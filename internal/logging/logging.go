// Package logging 是进程日志的统一入口：把配置里的级别字符串变成
// 一个可用的 slog.Logger，并装上默认 logger。
//
// 为什么用标准库 log/slog 而不是引入 zap/logrus：
//  1. Go 1.21 起 slog 是标准库，零新增第三方依赖——本项目从 P1 到 P6
//     一直刻意保持依赖面只有 gin/gorm/sqlite/yaml 四个，可观测性不
//     值得为它破例；
//  2. 需求是「结构化字段 + 级别过滤」，不是「极致性能」。slog 的
//     TextHandler 默认就是 key=value 形态，已经满足。
//
// 为什么用 SetDefault 而不是把 Logger 一层层注入：
//
//	日志是横切关注点，转发链路上每个包都要打（relay、scheduler、
//	middleware、store）。为了「不依赖全局状态」把 logger 透传进每个
//	函数签名，代价是十几个签名变长，收益只是测试里能换输出——而测试
//	需要捕获输出时可以直接再 SetDefault 一次（包级变量本来就可换）。
//	注入的适用边界是「有状态、可替换的业务依赖」（计费引擎、限流器），
//	日志不属这类。
package logging

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Setup 按级别字符串构建 logger 并设为 slog 的默认 logger。
//
// level 取值 debug/info/warn/error（大小写不敏感，config.Load 已经
// 统一成小写并做了白名单校验，这里再容错一次是因为 logging 不该
// 假设调用方一定过了 config 的校验——单测就会直接调它）。
//
// 输出走 stderr 而不是 stdout：Docker/K8s 采集的是容器的两个流，
// 但把日志混进 stdout 会和「本来要重定向到文件的内容」缠在一起；
// 而 stdout 留给 --seed-only 这类「结果是数据不是日志」的场景。
//
// AddSource 只在 debug 打开：文件名+行号对排查极有用，但每次调用
// 都要 runtime.Caller 取栈帧，info 级别常开是纯浪费。
func Setup(level string) (*slog.Logger, error) {
	var lvl slog.Level
	// TrimSpace：config.Load 已经归一过，但 logging 是叶子包，不该
	// 假设调用方一定过了归一（单测就直接调它）。slog 自己的
	// UnmarshalText 不接受带空格的 " info "，不 trim 会直接报错。
	if err := lvl.UnmarshalText([]byte(strings.TrimSpace(level))); err != nil {
		return nil, fmt.Errorf("非法日志级别 %q（支持 debug/info/warn/error）: %w", level, err)
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if lvl <= slog.LevelDebug {
		opts.AddSource = true
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, opts))
	slog.SetDefault(logger)
	return logger, nil
}
