// Package main 是网关的入口。
//
// 极简内核的设计原则：main.go 只做四件事
//  1. 加载配置（YAML 只剩 server 段 + database 段）
//  2. 打开数据库（空库时从 YAML 导入种子数据）
//  3. 用数据库里的渠道初始化转发层 + 注册路由
//  4. 启动 HTTP 服务
//
// 所有业务逻辑都在 internal/ 下，main.go 不出现任何业务代码。
// 这个「入口极薄」的写法抄自 one-api/main.go。
//
// P2 的变化：配置来源从 YAML 换成数据库。渠道和令牌此后只能通过
// 管理 API 变更，YAML 里的 channels/tokens 只在首次启动导入一次。
//
// P5 的变化：组装计费引擎（价格表缓存 + 额度扣减）注入转发层；
// 优雅关闭时先 srv.Shutdown 等在途请求，再由 Store.Close flush 异步日志。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"minigateway/internal/billing"
	"minigateway/internal/config"
	"minigateway/internal/model"
	"minigateway/internal/relay"
	"minigateway/internal/server"
	"minigateway/internal/server/middleware"
	"minigateway/internal/store"
)

func main() {
	// 用 flag 而不是写死路径，方便本地调试和 Docker 部署传参
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	// seedOnly 用于「只导种子数据不启动服务」的场景：先在初始化容器里
	// 跑一次把库建好，再由正式进程启动，避免两个进程抢同一个库。
	seedOnly := flag.Bool("seed-only", false, "只导入 YAML 种子数据后退出，不启动服务")
	flag.Parse()

	// 1. 加载配置
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	log.Printf("配置加载成功: 监听 %s, 数据库 %s", cfg.Server.Addr, cfg.Database.Path)

	// 2. 打开数据库并建表
	st, err := store.Open(cfg.Database)
	if err != nil {
		log.Fatalf("初始化数据库失败: %v", err)
	}
	// defer 保证任何退出路径都 flush 异步日志：Close 先排空日志队列
	// 再关连接，进程退出不丢日志（SPEC 2.4）
	defer st.Close()

	// 3. 空库时从 YAML 导入种子数据（幂等：非空库直接跳过）
	if _, err := st.SeedFromConfig(cfg); err != nil {
		log.Fatalf("导入种子数据失败: %v", err)
	}
	if *seedOnly {
		log.Println("种子数据导入完成，--seed-only 模式退出")
		return
	}

	// 4. 初始化转发层：渠道来自数据库快照，不再来自 YAML
	if err := relay.Init(st.Channels()); err != nil {
		log.Fatalf("初始化适配器失败: %v", err)
	}
	// 把运行参数注入转发层
	relay.SetRetryTimes(cfg.Server.RetryTimes)
	relay.SetTimeout(time.Duration(cfg.Server.TimeoutSeconds) * time.Second)
	// 记账：转发结束后把请求日志写进 logs 表（store 内部是异步批量
	// 写入，不阻塞响应）。注入而不是 relay 直接依赖 store，是为了让
	// 转发层保持纯粹——它不该知道数据存在哪，换存储甚至改成外发
	// 日志都不用改 relay。
	relay.SetLogRecorder(st.WriteLog)

	// 5. 组装计费引擎：价格表缓存从 store 加载，管理接口改价后主动
	// ReloadPrices 失效；额度扣减走 gorm 的原子 UPDATE（并发不丢）。
	priceTable := billing.NewPriceTable(func() ([]model.ModelPrice, map[string]float64, error) {
		prices, err := st.ModelPrices()
		if err != nil {
			return nil, nil, err
		}
		rates, err := st.GroupRates()
		if err != nil {
			return nil, nil, err
		}
		return prices, rates, nil
	}, billing.DefaultPriceTTL)
	engine := billing.New(st.DB(), priceTable)

	// 6. 创建引擎并注册路由
	// 管理 API 鉴权中间件在这里装配：密钥已由 config.Load 保证非空
	// （缺失时 Load 直接拒绝启动），中间件内部用常量时间比较校验
	// X-Admin-Key 头。/v1 转发面和 /healthz 不走这个中间件
	httpEngine := server.NewEngine(server.Deps{
		Cfg:       cfg,
		Store:     st,
		Billing:   engine,
		AdminAuth: middleware.AdminAuth(cfg.AdminKey),
	})

	srv := &http.Server{
		Addr:    cfg.Server.Addr,
		Handler: httpEngine,
	}

	// 7. 优雅关闭：收到信号后先停止接收新请求，给在途请求 10 秒收尾时间。
	//    网关持有上游连接，硬杀会导致客户端拿到半截响应。
	//    在途请求全部结束后 main 返回，defer 依次执行：st.Close() 先
	//    flush 异步日志（此时已无新日志入队）再关数据库——日志一条不丢。
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务异常退出: %v", err)
		}
	}()
	log.Printf("网关已启动: http://%s （渠道 %d 个，令牌 %d 个）",
		cfg.Server.Addr, relay.ChannelCount(), len(st.ListTokens()))
	log.Printf("管理接口: curl http://%s/api/channels", cfg.Server.Addr)
	log.Printf("试试转发: curl http://%s/v1/chat/completions -H 'Authorization: Bearer <你的令牌>' -H 'Content-Type: application/json' -d '{\"model\":\"<模型名>\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}'", cfg.Server.Addr)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("正在关闭网关...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("关闭超时: %v", err)
	}
	fmt.Println("网关已关闭")
}
