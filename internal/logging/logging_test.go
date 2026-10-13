package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// levelOf 探测当前默认 logger 的级别。
//
// slog.Logger 没有公开「当前级别」，但可以用 Enabled 逐级试探反推。
// 从低到高找到第一个被启用的级别就是当前阈值——这是在不引入
// 「把级别存到包级变量」这种额外状态的前提下，让 capture 用与生产
// 相同的阈值过滤（否则测试会把被级别挡掉的日志也算进断言，测的
// 就不是真实行为了）。
func levelOf(l *slog.Logger) slog.Level {
	for _, lvl := range []slog.Level{
		slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError,
	} {
		if l.Enabled(nil, lvl) {
			return lvl
		}
	}
	return slog.LevelError + 1 // 全关（实际上不会走到）
}

// capture 把默认 logger 的输出重定向到 buffer（阈值与当前默认 logger
// 一致），跑完 fn 后恢复。
func capture(t *testing.T, fn func()) string {
	t.Helper()
	prev := slog.Default()
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: levelOf(prev),
	})))
	defer slog.SetDefault(prev)
	fn()
	return buf.String()
}

// TestSetupSetsDefaultLogger 验证 Setup 真的换掉了全局默认 logger。
//
// 整个项目的日志都走 slog.Info/slog.Warn 这些包级函数（不传 logger），
// 所以「Setup 改了默认 logger」是级别配置能生效的唯一前提——这条
// 断了，log_level 配置就是个摆设。
func TestSetupSetsDefaultLogger(t *testing.T) {
	logger, err := Setup("debug")
	if err != nil {
		t.Fatalf("Setup 失败: %v", err)
	}
	if logger == nil {
		t.Fatal("返回的 logger 为 nil")
	}
	if !logger.Enabled(nil, slog.LevelDebug) {
		t.Error("debug 级别下 LevelDebug 应该被启用")
	}
}

// TestSetupLevelFiltering 验证级别真的在过滤。
//
// info 级别下 debug 日志必须不出现——这是 log_level=info 的承诺，
// 也是「debug 开满细节、info 只要摘要」这个设计能成立的原因。
func TestSetupLevelFiltering(t *testing.T) {
	if _, err := Setup("info"); err != nil {
		t.Fatalf("Setup 失败: %v", err)
	}
	out := capture(t, func() {
		slog.Debug("选凭据细节", "credential_id", 1)
		slog.Info("请求完成", "status", 200)
		slog.Warn("转发失败", "status", 502)
		slog.Error("结算失败", "err", "boom")
	})
	if strings.Contains(out, "选凭据细节") {
		t.Errorf("info 级别不该出现 debug 日志:\n%s", out)
	}
	for _, want := range []string{"请求完成", "转发失败", "结算失败"} {
		if !strings.Contains(out, want) {
			t.Errorf("info 级别应包含 %q，实际:\n%s", want, out)
		}
	}
}

// TestSetupWarnFiltersInfo 验证 warn 级别连 info 都不出。
//
// 单独测这一档：级别是「阈值」而不是「开关集合」，写错成
// 「只挡 debug」的实现会让 info 泄漏，在高流量下把日志刷爆。
func TestSetupWarnFiltersInfo(t *testing.T) {
	if _, err := Setup("warn"); err != nil {
		t.Fatalf("Setup 失败: %v", err)
	}
	out := capture(t, func() {
		slog.Info("请求完成", "status", 200)
		slog.Warn("转发失败", "status", 502)
	})
	if strings.Contains(out, "请求完成") {
		t.Errorf("warn 级别不该出现 info 日志:\n%s", out)
	}
	if !strings.Contains(out, "转发失败") {
		t.Errorf("warn 级别应包含 warn 日志:\n%s", out)
	}
}

// TestSetupDebugShowsDetail 验证 debug 级别能看到细节字段。
//
// B4-1 的验收要求：debug 能看到选凭据、冷却判定等细节。
func TestSetupDebugShowsDetail(t *testing.T) {
	if _, err := Setup("debug"); err != nil {
		t.Fatalf("Setup 失败: %v", err)
	}
	out := capture(t, func() {
		slog.Debug("[调度] 选中转发目标",
			"channel", "fake-primary", "credential_id", 7, "attempt", 0)
	})
	for _, want := range []string{"channel=fake-primary", "credential_id=7", "attempt=0"} {
		if !strings.Contains(out, want) {
			t.Errorf("debug 输出缺少字段 %q，实际:\n%s", want, out)
		}
	}
}

// TestSetupRejectsBadLevel 验证非法级别返回错误而不是静默接受。
//
// 静默接受会让 log_level=verbose 退化成某个默认级别，运维以为配了
// 却没生效——config.validate 已经在启动时拦过一道，这里再拦一次是
// 因为 logging 不该假设调用方一定过了 config 的校验（单测就直接调它）。
func TestSetupRejectsBadLevel(t *testing.T) {
	if _, err := Setup("verbose"); err == nil {
		t.Fatal("非法级别应该返回错误")
	}
}

// TestSetupCaseInsensitiveAndTrimmed 验证大小写和首尾空格都被容错。
//
// 配置文件里写 `log_level: " INFO"`（YAML 引号里带空格）并不出奇；
// config.Load 已经归一过一遍，但 logging 作为被单测直接调用的叶子包
// 不该要求调用方先归一——两次归一的成本是零，漏一次的代价是启动失败。
func TestSetupCaseInsensitiveAndTrimmed(t *testing.T) {
	for _, lvl := range []string{"INFO", "Info", "info", " info ", "DEBUG"} {
		if _, err := Setup(lvl); err != nil {
			t.Errorf("级别 %q 应该被接受，却报错: %v", lvl, err)
		}
	}
}

// TestStructuredFieldsRender 验证统一字段在输出里是可检索的 key=value。
//
// 结构化日志的全部价值在于「能按字段 grep/jq 过滤」，格式退化成
// 纯文本 msg 就白改了。request_id/token_id/channel_id/credential_id/
// model/latency_ms/status 这七个是 B4-1 规定的统一字段。
func TestStructuredFieldsRender(t *testing.T) {
	if _, err := Setup("info"); err != nil {
		t.Fatalf("Setup 失败: %v", err)
	}
	out := capture(t, func() {
		slog.Info("[请求] 完成",
			"request_id", "abc123",
			"token_id", 3,
			"channel_id", 1,
			"credential_id", 9,
			"model", "gpt-4o-mini",
			"latency_ms", 42,
			"status", 200)
	})
	for _, want := range []string{
		"request_id=abc123", "token_id=3", "channel_id=1",
		"credential_id=9", "model=gpt-4o-mini", "latency_ms=42", "status=200",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("统一字段 %q 没出现在输出里:\n%s", want, out)
		}
	}
}
