package resilience

import (
	"strings"
	"testing"

	prom "github.com/prometheus/client_golang/prometheus"
)

// TestRegisterReuse_TypeMismatch_ActionablePanic 回归测试：同名同描述符但
// **类型不同**时，panic 的信息必须能定位问题。
//
// 缺陷成因：既有 collector 与请求的 collector 描述符相同（fqName + help +
// 标签名都一致）但类型不同 ⇒ prometheus 返回 AlreadyRegisteredError（不是
// 直接拒绝）⇒ `are.ExistingCollector.(T)` 是一次**无检查的类型断言** ⇒
// panic 成一句不可定位的
// "interface conversion: prometheus.Collector is *prometheus.CounterVec,
// not *prometheus.GaugeVec"——看不出「谁该改成谁」。
func TestRegisterReuse_TypeMismatch_ActionablePanic(t *testing.T) {
	reg := prom.NewRegistry()
	// 描述符与 resilience 自己的 breaker_state 完全一致，但类型是 CounterVec。
	reg.MustRegister(prom.NewCounterVec(prom.CounterOpts{
		Name:      "breaker_state",
		Namespace: "resilience",
		Help:      "Circuit breaker state: 0=closed 1=half_open 2=open.",
	}, []string{"service", "kind"}))

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("类型不符时应 panic")
		}
		msg, ok := r.(error)
		if !ok {
			t.Fatalf("panic 值应为 error，实得 %T（%v）", r, r)
		}
		text := msg.Error()
		if !strings.Contains(text, "类型不符") {
			t.Errorf("panic 信息未说明「类型不符」：%q", text)
		}
		// 必须同时给出既有类型与请求类型。
		if !strings.Contains(text, "*prometheus.CounterVec") ||
			!strings.Contains(text, "*prometheus.GaugeVec") {
			t.Errorf("panic 信息未同时给出既有类型与请求类型：%q", text)
		}
	}()

	_ = NewMetrics(reg, "resilience")
}

// TestRegisterReuse_SameType_ReusesExisting 同类型同名 ⇒ 复用既有实例
// （既有口径，回归护栏）。
func TestRegisterReuse_SameType_ReusesExisting(t *testing.T) {
	reg := prom.NewRegistry()
	first := NewMetrics(reg, "resilience")
	second := NewMetrics(reg, "resilience")
	if first == second {
		t.Fatal("两次 NewMetrics 应返回各自的实例（复用的是 collector 而非 Metrics 结构体）")
	}
	// collector 必须被复用（否则重复注册会报错）——这里能正常构造即说明复用成功。
	first.ObserveBreakerState("svc", "httpx", 1)
	second.ObserveBreakerState("svc", "httpx", 2)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, mf := range mfs {
		if mf.GetName() == "resilience_breaker_state" {
			found = true
			if len(mf.GetMetric()) != 1 {
				t.Errorf("同名 collector 未被复用，产生了 %d 条 series", len(mf.GetMetric()))
			}
		}
	}
	if !found {
		t.Fatal("未找到 resilience_breaker_state")
	}
}
