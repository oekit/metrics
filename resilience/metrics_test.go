package resilience

import (
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
		99:            "closed", // 未知值保守归 closed
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
