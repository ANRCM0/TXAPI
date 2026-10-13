package metrics

import (
	"strings"
	"testing"
	"time"
)

// 本文件验证 exposition 输出「真的能被 Prometheus 抓取」——格式错误
// 的指标不会报错，只会静默地从监控里消失，所以逐项断言格式而不是
// 只断言「有输出」。

func render(t *testing.T, r *Registry) string {
	t.Helper()
	var b strings.Builder
	if err := r.Render(&b); err != nil {
		t.Fatalf("Render 失败: %v", err)
	}
	return b.String()
}

// TestCounterFormat 验证 counter 的三个必备要素：_total 后缀、
// # TYPE counter、以及带标签的数值行。
//
// 缺 # TYPE 行 Prometheus 会按 untyped 处理（能存不能 rate），
// 后缀写错则 rate() 直接查不到——两者都是「指标存在但没用」。
func TestCounterFormat(t *testing.T) {
	r := New()
	r.RecordRequest("gpt-4o-mini", "fake-primary", 200, 10*time.Millisecond)
	r.RecordRequest("gpt-4o-mini", "fake-primary", 200, 10*time.Millisecond)
	r.RecordRequest("gpt-4o-mini", "fake-primary", 500, 10*time.Millisecond)

	out := render(t, r)
	for _, want := range []string{
		"# HELP " + MetricRequestsTotal,
		"# TYPE " + MetricRequestsTotal + " counter",
		`gateway_requests_total{channel="fake-primary",model="gpt-4o-mini",status="200"} 2`,
		`gateway_requests_total{channel="fake-primary",model="gpt-4o-mini",status="500"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q，实际:\n%s", want, out)
		}
	}
	// 标签顺序稳定（字典序）：Prometheus 不要求，但让两次抓取的
	// 输出可 diff，排查「指标值变了」时才有意义。
	first := strings.Index(out, MetricRequestsTotal+"{")
	line := out[first:]
	if !strings.HasPrefix(line, `gateway_requests_total{channel=`) {
		t.Errorf("标签顺序不稳定，期望 channel 在前，实际: %s", firstLine(line))
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// TestHistogramFormat 验证 histogram 的四件套：每个 le 一个 bucket
// （含 +Inf）、_sum、_count，且 bucket 是**累计**值。
//
// 累计是 histogram_quantile() 能工作的前提；写成「每桶独立计数」
// 的话 Prometheus 不报错，但算出来的分位数全是错的。
func TestHistogramFormat(t *testing.T) {
	r := New()
	// 三次观测分别落在 0.05s 档内、0.25s 档内、超过最大桶（60s）
	r.RecordRequest("m", "c", 200, 20*time.Millisecond)
	r.RecordRequest("m", "c", 200, 200*time.Millisecond)
	r.RecordRequest("m", "c", 200, 90*time.Second)

	out := render(t, r)
	if !strings.Contains(out, "# TYPE "+MetricDurationSeconds+" histogram") {
		t.Errorf("缺少 histogram 的 TYPE 行:\n%s", out)
	}
	// 累计：le=0.05 有 1 个，le=0.25 有 2 个（含前面那个），
	// 最大桶 60 仍然只有 2 个，+Inf 有全部 3 个。
	//
	// 注意 le 与其他标签在同一对花括号内且整体按键名排序——
	// 写成 `{...},le="0.05"` 是非法格式，Prometheus 会整行报错。
	cases := []struct{ le, want string }{
		{"0.05", `gateway_request_duration_seconds_bucket{channel="c",le="0.05",model="m"} 1`},
		{"0.25", `gateway_request_duration_seconds_bucket{channel="c",le="0.25",model="m"} 2`},
		{"60", `gateway_request_duration_seconds_bucket{channel="c",le="60",model="m"} 2`},
		{"+Inf", `gateway_request_duration_seconds_bucket{channel="c",le="+Inf",model="m"} 3`},
	}
	for _, c := range cases {
		if !strings.Contains(out, c.want) {
			t.Errorf("bucket %s 累计值不对，期望 %q，实际:\n%s", c.le, c.want, out)
		}
	}
	if !strings.Contains(out, `gateway_request_duration_seconds_count{channel="c",model="m"} 3`) {
		t.Errorf("_count 不对:\n%s", out)
	}
	// _sum = 0.02 + 0.2 + 90 = 90.22
	if !strings.Contains(out, `gateway_request_duration_seconds_sum{channel="c",model="m"} 90.220000`) {
		t.Errorf("_sum 不对:\n%s", out)
	}
	// 桶必须按 le 升序出现
	pos := []int{
		strings.Index(out, `le="0.05"`),
		strings.Index(out, `le="0.25"`),
		strings.Index(out, `le="+Inf"`),
	}
	if !(pos[0] < pos[1] && pos[1] < pos[2]) {
		t.Errorf("bucket 未按 le 升序: %v", pos)
	}
}

// TestTokensAndCostFormat 验证 token 的 type 标签和费用的浮点格式。
func TestTokensAndCostFormat(t *testing.T) {
	r := New()
	r.RecordTokens("m", "c", TokenTypePrompt, 11)
	r.RecordTokens("m", "c", TokenTypePrompt, 4)
	r.RecordTokens("m", "c", TokenTypeCompletion, 7)
	r.RecordCost("m", "c", 0.00585)

	out := render(t, r)
	for _, want := range []string{
		`gateway_tokens_total{channel="c",model="m",type="prompt"} 15`,
		`gateway_tokens_total{channel="c",model="m",type="completion"} 7`,
		// 费用用微元整数累加再除回元，避免浮点累加误差
		`gateway_cost_total{channel="c",model="m"} 0.005850`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q，实际:\n%s", want, out)
		}
	}
	// 0 用量不该产生指标行：会产生大量 value 0 的序列，撑大抓取体积
	// 且让「有没有这个模型的流量」变得看不出来。
	r2 := New()
	r2.RecordTokens("m", "c", TokenTypePrompt, 0)
	if strings.Contains(render(t, r2), MetricTokensTotal+"{") {
		t.Error("0 用量被记成了指标行")
	}
}

// TestCredentialGaugeFormat 验证 gauge 用 provider 惰性求值。
//
// gauge 必须现算而不是累加：一把 Key 从 cooldown 回 active 是
// 「一档 -1、另一档 +1」，累加式表达不了这种状态迁移。
func TestCredentialGaugeFormat(t *testing.T) {
	r := New()
	r.SetCredentialStatsProvider(func() []CredentialStat {
		return []CredentialStat{
			{Channel: "ch1", Status: "active", Count: 2},
			{Channel: "ch1", Status: "cooldown", Count: 1},
		}
	})
	out := render(t, r)
	if !strings.Contains(out, "# TYPE "+MetricCredentials+" gauge") {
		t.Errorf("gauge 的 TYPE 行不对:\n%s", out)
	}
	for _, want := range []string{
		`gateway_credentials{channel="ch1",status="active"} 2`,
		`gateway_credentials{channel="ch1",status="cooldown"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q，实际:\n%s", want, out)
		}
	}
}

// TestCredentialFailureCounter 验证凭据失败指标的标签（channel + credential）。
func TestCredentialFailureCounter(t *testing.T) {
	r := New()
	r.RecordCredentialFailure("ch1", 42)
	r.RecordCredentialFailure("ch1", 42)
	out := render(t, r)
	want := `gateway_credential_failures_total{channel="ch1",credential="42"} 2`
	if !strings.Contains(out, want) {
		t.Errorf("输出缺少 %q，实际:\n%s", want, out)
	}
}

// TestLabelEscaping 验证标签值里的引号/反斜杠/换行被转义。
//
// 不转义会生成抓取端解析失败的整体输出——故障表现是「整个 /metrics
// 抓不上来」，比少一个指标严重得多。渠道名带引号虽然罕见，但
// 模型名里出现它们并不出奇（用户自建的模型名）。
func TestLabelEscaping(t *testing.T) {
	r := New()
	r.RecordRequest(`mo"del\path`, "ch\nname", 200, time.Millisecond)
	out := render(t, r)
	want := `gateway_requests_total{channel="ch\nname",model="mo\"del\\path",status="200"} 1`
	if !strings.Contains(out, want) {
		t.Errorf("标签转义不对:\n期望 %q\n实际:\n%s", want, out)
	}
}

// TestDeterministicOutput 验证同一状态下两次抓取逐字节相同。
//
// Go 的 map 遍历顺序是随机的，不排序的话每次抓取的指标行顺序都不同，
// diff 工具完全没法用，且会让「指标值有没有变」的人工比对变成不可能。
func TestDeterministicOutput(t *testing.T) {
	r := New()
	for _, m := range []string{"b-model", "a-model", "c-model"} {
		r.RecordRequest(m, "ch", 200, time.Millisecond)
	}
	a, b := render(t, r), render(t, r)
	if a != b {
		t.Error("同一状态下两次抓取输出不一致（未排序）")
	}
}

// TestConcurrentRecord 用 -race 验证并发埋点不丢计数。
//
// 这是「请求路径不加锁」设计的回归测试：计数器用 atomic，建表用
// 双检锁。任何一处漏了都会在 -race 或这里的计数断言上暴露。
func TestConcurrentRecord(t *testing.T) {
	r := New()
	const goroutines, perG = 8, 200
	done := make(chan struct{})
	for g := 0; g < goroutines; g++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < perG; i++ {
				r.RecordRequest("m", "ch", 200, time.Millisecond)
			}
		}()
	}
	for g := 0; g < goroutines; g++ {
		<-done
	}
	out := render(t, r)
	want := `gateway_requests_total{channel="ch",model="m",status="200"} ` +
		itoa(goroutines*perG)
	if !strings.Contains(out, want) {
		t.Errorf("并发计数丢失，期望 %q，实际:\n%s", want, out)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}

// TestEmptyRegistry 验证没有任何埋点时输出仍然合法（只有 HELP/TYPE）。
//
// 空输出会让 Prometheus 报 "no metric found"，target 直接 down——
// 新装的网关在第一个请求之前必须已经是可抓取状态。
func TestEmptyRegistry(t *testing.T) {
	out := render(t, New())
	for _, name := range []string{
		MetricRequestsTotal, MetricDurationSeconds, MetricTokensTotal,
		MetricCostTotal, MetricCredentials, MetricCredFailures,
	} {
		if !strings.Contains(out, "# TYPE "+name+" ") {
			t.Errorf("空注册表缺少 %s 的 TYPE 行:\n%s", name, out)
		}
	}
}
