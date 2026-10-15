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
//
// P6 批次 4 的变化：日志换成结构化 log/slog（级别来自配置），转发路径
// 上埋 Prometheus 指标（GET /metrics 暴露），并启动日志表清理任务。
// 三者的共同点是「不影响功能正确性，只影响能不能排查」——所以接线
// 都放在最外层，任何一个出问题都不该拦住启动。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"minigateway/internal/billing"
	"minigateway/internal/config"
	"minigateway/internal/gateway"
	"minigateway/internal/logging"
	"minigateway/internal/metrics"
	"minigateway/internal/model"
	"minigateway/internal/scheduler"
	"minigateway/internal/server"
	"minigateway/internal/server/middleware"
	"minigateway/internal/store"
)

// EnvLogCleanupInterval 覆盖日志清理间隔（Go duration，如 "1h"）。
//
// 为什么值得留这个环境变量：保留天数调小后，运维想立刻看到库开始缩，
// 而不是等最多 24 小时；排查清理逻辑本身也更省时间。清理是幂等的，
// 所以间隔只影响收敛速度，不影响正确性。
const EnvLogCleanupInterval = "GATEWAY_LOG_CLEANUP_INTERVAL"

func main() {
	// 用 flag 而不是写死路径，方便本地调试和 Docker 部署传参
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	// seedOnly 用于「只导种子数据不启动服务」的场景：先在初始化容器里
	// 跑一次把库建好，再由正式进程启动，避免两个进程抢同一个库。
	seedOnly := flag.Bool("seed-only", false, "只导入 YAML 种子数据后退出，不启动服务")
	initAdmin := flag.Bool("init-admin", false, "create the first admin from TXAPI_ADMIN_USERNAME/TXAPI_ADMIN_PASSWORD and exit")
	claimToken := flag.Uint("claim-token", 0, "claim one unowned legacy token ID and exit")
	claimOwner := flag.Uint("owner-user", 0, "active user ID for -claim-token")
	flag.Parse()

	// 1. 加载配置
	cfg, err := config.Load(*configPath)
	if err != nil {
		// 配置加载失败时结构化 logger 还没建（级别就在配置里），
		// 只能用 slog 的默认 logger——它在 Setup 之前是零值
		// TextHandler，输出可用但不带级别过滤。启动即失败的情况
		// 下这完全够用。
		slog.Error("加载配置失败", "config", *configPath, "err", err)
		os.Exit(1)
	}
	// 结构化日志的初始化放在配置之后、任何业务之前：级别来自配置，
	// 后面每一行日志都要走它。用 os.Exit 而不是 log.Fatal：此时还没有
	// 需要 defer 清理的资源（数据库还没开），不需要跑 defer 链。
	if _, err := logging.Setup(cfg.LogLevel); err != nil {
		slog.Error("初始化日志失败", "level", cfg.LogLevel, "err", err)
		os.Exit(1)
	}
	slog.Info("配置加载成功",
		"addr", cfg.Server.Addr, "database", cfg.Database.Path,
		"log_level", cfg.LogLevel, "log_retention_days", cfg.LogRetentionDays)

	// 2. 打开数据库并建表
	st, err := store.Open(cfg.Database)
	if err != nil {
		slog.Error("初始化数据库失败", "err", err)
		os.Exit(1)
	}
	// defer 保证任何退出路径都 flush 异步日志：Close 先排空日志队列
	// 再关连接，进程退出不丢日志（SPEC 2.4）
	defer st.Close()
	if *initAdmin {
		user, err := st.BootstrapAdmin(os.Getenv("TXAPI_ADMIN_USERNAME"), os.Getenv("TXAPI_ADMIN_PASSWORD"))
		if err != nil {
			slog.Error("admin initialization failed", "err", err)
			os.Exit(1)
		}
		slog.Info("administrator initialized", "user_id", user.ID)
		return
	}
	if *claimToken != 0 {
		token, err := st.ClaimToken(*claimToken, *claimOwner)
		if err != nil {
			slog.Error("legacy key claim failed", "err", err)
			os.Exit(1)
		}
		slog.Info("legacy key claimed", "token_id", token.ID, "user_id", token.UserID)
		return
	}

	// 3. 空库时从 YAML 导入种子数据（幂等：非空库直接跳过）
	if _, err := st.SeedFromConfig(cfg); err != nil {
		slog.Error("导入种子数据失败", "err", err)
		os.Exit(1)
	}
	if *seedOnly {
		slog.Info("种子数据导入完成，--seed-only 模式退出")
		return
	}

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

	limiter := middleware.NewMemoryLimiter()
	var gw *gateway.Gateway
	health := scheduler.NewHealthWorker(func(u scheduler.HealthUpdate) error {
		return st.UpdateCredentialHealth(store.HealthUpdate{
			CredentialID:  u.CredentialID,
			FailCount:     u.FailCount,
			CooldownUntil: u.CooldownUntil,
			Status:        u.Status,
			LastError:     u.LastError,
			LastUsedAt:    u.LastUsedAt,
		})
	}, func() {
		if err := gw.Reload(st.Channels()); err != nil {
			// 重载失败必须告警：状态改了但转发层没看到，等于这次健康
			// 反馈白做，坏 Key 还会继续被选中
			slog.Error("[调度] 凭据状态变更后刷新渠道池失败", "err", err)
		}
	})
	defer health.Close()
	gw, err = gateway.New(gateway.Options{
		Channels: st.Channels(), RetryTimes: cfg.Server.RetryTimes,
		AttemptTimeout: time.Duration(cfg.Server.TimeoutSeconds) * time.Second,
		TotalTimeout:   time.Duration(cfg.Server.TotalTimeoutSeconds) * time.Second,
		Billing:        engine, Limiter: limiter, Metrics: metrics.Default, LogRecorder: st.WriteLog,
		Picker: func(creds []model.Credential, exclude map[uint]bool, sessionID string, now time.Time) (*model.Credential, bool) {
			return scheduler.PickWithAffinity(creds, sessionID, exclude, now)
		},
		HealthReporter: func(cred *model.Credential, statusCode int, errMsg string) {
			// 成功/失败都走这里：scheduler 内部按状态码决定是清零还是退避，
			// 并在状态真的变化时才落库（见 scheduler.HealthUpdate.Changed）。
			now := time.Now()
			if statusCode == http.StatusOK {
				health.RecordSuccess(cred, now)
				return
			}
			health.RecordFailure(cred, statusCode, errMsg, now)
		},
	})
	if err != nil {
		slog.Error("gateway initialization failed", "err", err)
		os.Exit(1)
	}
	defer gw.Close()

	// 后台恢复任务：定时把冷却到期的凭据放回 active（banned 不自动恢复）。
	// 停止函数接在优雅关闭链路上——顺序是 srv.Shutdown → stopRecovery →
	// health.Close → st.Close，保证恢复任务不再读库之后才关库。
	stopRecovery := scheduler.StartRecovery(func() (int, error) {
		return st.RecoverCooledDownCredentials()
	}, scheduler.RecoveryInterval)
	defer stopRecovery()

	// 8. 日志表清理任务：定时删一批过期的请求日志。
	//
	// 为什么需要：logs 表每请求一行且只增不减，SQLite 单文件会越用
	// 越慢（插入和统计都要扫更大的 b-tree），而且没有任何显式故障
	// ——「网关用久了变卡」是最难排查的一类退化。
	//
	// 间隔默认 24h（scheduler.CleanupInterval），可用
	// GATEWAY_LOG_CLEANUP_INTERVAL 覆盖成 Go duration（如 1h、30m）。
	// 为什么值得留这个口子：保留天数调小之后，运维会想立刻看到库
	// 开始缩，而不是等最多 24 小时；调试清理逻辑本身也更省时间。
	// 它不影响正确性（清理是幂等的），只影响「多快收敛」。
	//
	// 停止函数接在优雅关闭链路上，必须在 st.Close() **之前**停：
	// defer 是 LIFO，这里写在 st.Close 的 defer 之后注册，所以会
	// 先执行。顺序反了的话清理任务会在库已关闭后还去写它。
	cleanupInterval := scheduler.CleanupInterval
	if v := strings.TrimSpace(os.Getenv(EnvLogCleanupInterval)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			// 配错不拒绝启动：清理是可观测性的附属功能，为它拦住
			// 网关启动不值得；回落默认值并响亮告警即可。
			slog.Warn("日志清理间隔配置非法，使用默认值",
				"env", EnvLogCleanupInterval, "value", v,
				"default", cleanupInterval.String(), "err", err)
		} else if d > 0 {
			cleanupInterval = d
		} else {
			slog.Warn("日志清理间隔必须为正数，使用默认值",
				"env", EnvLogCleanupInterval, "value", v, "default", cleanupInterval.String())
		}
	}
	stopLogCleanup := scheduler.StartLogCleanup(func() (int, error) {
		return st.PurgeOnce(cfg.LogRetentionDays)
	}, cleanupInterval)
	defer stopLogCleanup()
	if cfg.LogRetentionDays > 0 {
		slog.Info("日志清理任务已启动",
			"retention_days", cfg.LogRetentionDays, "interval", cleanupInterval.String())
	} else {
		// 明确说出来：静默不启动会让运维以为在清理，直到库爆了才发现
		slog.Info("日志自动清理已关闭（log_retention_days=0），logs 表将无限增长")
	}

	// 6. 限流器：单机内存版滑动窗口，令牌级（RPM/TPM）+ IP 级兜底。
	// 阈值来自数据库里每个令牌的 rpm_limit/tpm_limit 字段，管理 API
	// 改完即生效（限流中间件每次请求都从上下文读投影，不缓存阈值）。
	// 多机部署时把这一行换成 Redis 实现即可，中间件不用改。
	// 优雅关闭时先停限流：在途请求收尾期间不该再有新请求挤进来
	// 重新计数（见 middleware.Limiter.Close 的注释）。
	defer func() {
		if closer, ok := limiter.(interface{ Close() }); ok {
			closer.Close()
		}
	}()

	// 7. 创建引擎并注册路由
	// 管理 API 鉴权中间件在这里装配：密钥已由 config.Load 保证非空
	// （缺失时 Load 直接拒绝启动），中间件内部用常量时间比较校验
	// X-Admin-Key 头。/v1 转发面和 /healthz 不走这个中间件
	httpEngine := server.NewEngine(server.Deps{
		Cfg:         cfg,
		Gateway:     gw,
		Store:       st,
		Billing:     engine,
		RateLimiter: limiter,
		// IP 级兜底限流：防「拿随机 key 试」的探测请求（那种请求
		// 过不了鉴权，令牌级限流根本轮不到它）。阈值取自配置，
		// 0 = 不限。默认 0——自用场景（本机/内网单客户端）没有
		// 探测流量，开了反而可能挡掉正常的并发压测；有暴露面时
		// 在 config.yaml 的 server 段配上即可。
		IPRPMLimit: cfg.Server.IPRPMLimit,
		AdminAuth:  middleware.AdminAuth(cfg.AdminKey),
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
			slog.Error("HTTP 服务异常退出", "err", err)
			os.Exit(1)
		}
	}()
	slog.Info("网关已启动", "url", "http://"+cfg.Server.Addr,
		"channels", gw.ChannelCount(), "tokens", len(st.ListTokens()),
		"metrics", "http://"+cfg.Server.Addr+"/metrics")
	slog.Info("管理接口", "url", "http://"+cfg.Server.Addr+"/api/channels")
	slog.Info("试试转发",
		"cmd", fmt.Sprintf("curl http://%s/v1/chat/completions -H 'Authorization: Bearer <令牌>' -H 'Content-Type: application/json' -d '{\"model\":\"<模型名>\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}'", cfg.Server.Addr))

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("正在关闭网关...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("关闭超时，强制退出", "err", err)
		os.Exit(1)
	}
	fmt.Println("网关已关闭")
}
