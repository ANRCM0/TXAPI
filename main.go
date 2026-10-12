// Package main 是网关的入口。
//
// 极简内核的设计原则：main.go 只做三件事
//   1. 加载配置
//   2. 创建引擎并注册路由
//   3. 启动 HTTP 服务
// 所有业务逻辑都在 internal/ 下，main.go 不出现任何业务代码。
// 这个「入口极薄」的写法抄自 one-api/main.go。
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

	"minigateway/internal/config"
	"minigateway/internal/relay"
	"minigateway/internal/server"
)

func main() {
	// 用 flag 而不是写死路径，方便本地调试和 Docker 部署传参
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	flag.Parse()

	// 1. 加载配置
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	log.Printf("配置加载成功: 监听 %s, 共 %d 个渠道, %d 个令牌",
		cfg.Server.Addr, len(cfg.Channels), len(cfg.Tokens))

	// 2. 初始化适配器注册表（把渠道配置变成可用的适配器实例）
	if err := relay.Init(cfg); err != nil {
		log.Fatalf("初始化适配器失败: %v", err)
	}
	// 把运行参数注入转发层
	relay.SetRetryTimes(cfg.Server.RetryTimes)
	relay.SetTimeout(time.Duration(cfg.Server.TimeoutSeconds) * time.Second)

	// 3. 创建引擎并注册路由
	engine := server.NewEngine(cfg)

	srv := &http.Server{
		Addr:    cfg.Server.Addr,
		Handler: engine,
	}

	// 4. 优雅关闭：收到信号后先停止接收新请求，给在途请求 10 秒收尾时间。
	//    网关持有上游连接，硬杀会导致客户端拿到半截响应。
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务异常退出: %v", err)
		}
	}()
	log.Printf("网关已启动: http://%s", cfg.Server.Addr)
	log.Printf("试试: curl http://%s/v1/chat/completions -H 'Authorization: Bearer sk-test-123' -H 'Content-Type: application/json' -d '{\"model\":\"gpt-4o-mini\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}'", cfg.Server.Addr)

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
