package resilience

import (
	"fmt"
	"strings"
	"testing"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func newTestMetrics() (*Metrics, *prom.Registry) {
	reg := prom.NewRegistry()
	return NewMetrics(reg, "test"), reg
}

// TestMetrics_Observations 验证四类观测落到正确的 metric series。
func TestMetrics_Observations(t *testing.T) {
	m, _ := newTestMetrics()

	m.ObserveBreakerState("svc-a", "httpx", StateClosed)
	if got := testutil.ToFloat64(m.breakerState.WithLabelValues("svc-a", "httpx")); got != 0 {
		t.Fatalf("expected closed state 0, got %v", got)
	}

	m.ObserveTransition("svc-a", "httpx", StateClosed, StateOpen)
	if got := testutil.ToFloat64(m.breakerState.WithLabelValues("svc-a", "httpx")); got != 2 {
		t.Fatalf("expected open state 2 after transition, got %v", got)
	}
	if got := testutil.ToFloat64(m.breakerTransitions.WithLabelValues("svc-a", "httpx", "closed", "open")); got != 1 {
		t.Fatalf("expected 1 transition, got %v", got)
	}

	m.ObserveRejected("svc-a", "httpx")
	m.ObserveRejected("svc-a", "httpx")
	if got := testutil.ToFloat64(m.breakerRejected.WithLabelValues("svc-a", "httpx")); got != 2 {
		t.Fatalf("expected 2 rejections, got %v", got)
	}

	m.ObserveRetry("svc-a", "httpx")
	if got := testutil.ToFloat64(m.retryAttempts.WithLabelValues("svc-a", "httpx")); got != 1 {
		t.Fatalf("expected 1 retry, got %v", got)
	}
}

// TestStateName 验证状态 label 值映射。
func TestStateName(t *testing.T) {
	cases := map[int]string{
		StateClosed:   "closed",
		StateHalfOpen: "half_open",
		StateOpen:     "open",
		99:            "open", // 未知值 fail-loud 归 open，不谎报健康（#66）
	}
	for state, want := range cases {
		if got := StateName(state); got != want {
			t.Errorf("StateName(%d) = %q, want %q", state, got, want)
		}
	}
}

// TestNewMetrics_RegistryReuse 同一 Registry + namespace 重复创建复用
// collector（多 Client/工厂共享指标的常态路径），不 panic。
func TestNewMetrics_RegistryReuse(t *testing.T) {
	reg := prom.NewRegistry()
	a := NewMetrics(reg, "dup")
	b := NewMetrics(reg, "dup")
	if a.breakerState != b.breakerState {
		t.Fatal("expected reuse of already-registered breaker_state collector")
	}
	a.ObserveRetry("svc", "httpx")
	if got := testutil.ToFloat64(b.retryAttempts.WithLabelValues("svc", "httpx")); got != 1 {
		t.Fatalf("expected shared collector visible across instances, got %v", got)
	}
}

// TestNewMetrics_Defaults 验证 reg/namespace 缺省回退。
func TestNewMetrics_Defaults(t *testing.T) {
	if NewMetrics(nil, "") == nil {
		t.Fatal("expected non-nil metrics with defaults")
	}
}

// —— 标签基数防护（#105）——

// mustPanic 收编期望中的 fail-fast panic，返回 panic 值（非 panic 即失败）。
func mustPanic(t *testing.T, what string, fn func()) (msg string) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("%s：期望 fail-fast panic，实际正常返回", what)
		}
		s, ok := r.(string)
		if !ok {
			t.Fatalf("%s：panic 值应为 string，got %T", what, r)
		}
		msg = s
	}()
	fn()
	return
}

// TestNewMetrics_LabelLimitFailFast 超限新组合 fail-fast（#105 防护口径：
// 上限经 WithLabelLimit 构造期配置，超限 panic 报错而非静默丢弃打点）。
func TestNewMetrics_LabelLimitFailFast(t *testing.T) {
	m := NewMetrics(prom.NewRegistry(), "probe", WithLabelLimit(2))

	// breaker_state family：前 2 个新组合放行，第 3 个 panic
	m.ObserveBreakerStateTyped("svc1", "httpx", CircuitStateClosed)
	m.ObserveBreakerStateTyped("svc2", "httpx", CircuitStateClosed)
	msg := mustPanic(t, "第 3 个组合", func() {
		m.ObserveBreakerStateTyped("svc3", "httpx", CircuitStateClosed)
	})
	if !strings.Contains(msg, "breaker_state") || !strings.Contains(msg, "上限 2") {
		t.Fatalf("panic 消息应携带 family 与上限，got: %s", msg)
	}
	// 已见组合在超限后照常放行（记账的是「新组合」，不是「写入次数」）
	m.ObserveBreakerStateTyped("svc1", "httpx", CircuitStateOpen)

	// 各 family 独立记账：breaker_state 已满，retry_attempts 仍有自己的预算
	m.ObserveRetry("svc-fresh", "httpx")
	m.ObserveRejected("svc-fresh", "httpx")

	// transitions 按（service,kind,from,to) 全组合记账
	m2 := NewMetrics(prom.NewRegistry(), "probe", WithLabelLimit(2))
	m2.ObserveTransitionTyped("s", "httpx", CircuitStateClosed, CircuitStateOpen)
	m2.ObserveTransitionTyped("s", "httpx", CircuitStateOpen, CircuitStateClosed)
	mustPanic(t, "transitions 第 3 个组合", func() {
		m2.ObserveTransitionTyped("s", "httpx", CircuitStateOpen, CircuitStateOpen)
	})
	// 已见组合重放不 panic
	m2.ObserveTransitionTyped("s", "httpx", CircuitStateClosed, CircuitStateOpen)
}

// TestNewMetrics_LabelLimitDefaultFallback n<=0 与缺省构造回退
// DefaultLabelLimit（nil/零值参数 = 显式回退默认值，总则②）：写满
// DefaultLabelLimit 个组合不 panic，第 DefaultLabelLimit+1 个 panic。
func TestNewMetrics_LabelLimitDefaultFallback(t *testing.T) {
	for _, opts := range [][]MetricsOption{nil, {WithLabelLimit(0)}, {WithLabelLimit(-5)}, {nil}} {
		m := NewMetrics(prom.NewRegistry(), "probe", opts...)
		for i := 0; i < DefaultLabelLimit; i++ {
			m.ObserveRetry(fmt.Sprintf("svc-%d", i), "httpx")
		}
		mustPanic(t, "超出缺省上限", func() {
			m.ObserveRetry(fmt.Sprintf("svc-%d", DefaultLabelLimit), "httpx")
		})
	}
}
