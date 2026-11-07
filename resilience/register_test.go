package resilience

import (
	"bufio"
	"os"

	"strings"
	"testing"

	"github.com/oekit/metrics"
	prom "github.com/prometheus/client_golang/prometheus"
)

// TestRegisterReuse_TypeMismatch_DegradesObservably 回归测试（#44）：同名
// 同描述符但**类型不同**时降级为独立实例 + stderr WARN——此前是硬断言 panic：
// Registry 是进程级共享面，撞名是运行期组合态（消费方先注册过同名 GaugeVec
// 再换 CounterVec，或两个子系统共享 Registry 撞名），一个子系统炸死整个进程
// 不成比例。「其余错误仍 panic」的边界不变（Register 不可用等照旧）。
func TestRegisterReuse_TypeMismatch_DegradesObservably(t *testing.T) {
	reg := prom.NewRegistry()
	// 描述符与 resilience 自己的 breaker_state 完全一致，但类型是 CounterVec。
	reg.MustRegister(prom.NewCounterVec(prom.CounterOpts{
		Name:      "breaker_state",
		Namespace: "resilience",
		Help:      "Circuit breaker state: 0=closed 1=half_open 2=open.",
	}, []string{"service", "kind"}))

	// stderr WARN 可观测
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = w

	got := registerReuse(reg, prom.NewGaugeVec(prom.GaugeOpts{
		Name:      "breaker_state",
		Namespace: "resilience",
		Help:      "Circuit breaker state: 0=closed 1=half_open 2=open.",
	}, []string{"service", "kind"}))

	w.Close()
	os.Stderr = oldStderr
	sc := bufio.NewScanner(r)
	warnSeen := false
	for sc.Scan() {
		if strings.Contains(sc.Text(), "同名 collector 类型不符") {
			warnSeen = true
			break
		}
	}
	if !warnSeen {
		t.Fatal("类型不符降级时必须打 stderr WARN")
	}
	if got == nil {
		t.Fatal("降级路径必须返回可用实例")
	}
	// 再注册同名（registry 里占位仍是 CounterVec）⇒ 再次命中类型不符 ⇒
	// 再次降级：不 panic、返回另一个独立实例。降级模式**不复用也不幂等**
	// （每次调用一个新序列集）——这是降级的代价，换进程存活；命名修复后
	// 即恢复正常复用路径。
	again := registerReuse(reg, prom.NewGaugeVec(prom.GaugeOpts{
		Name:      "breaker_state",
		Namespace: "resilience",
		Help:      "Circuit breaker state: 0=closed 1=half_open 2=open.",
	}, []string{"service", "kind"}))
	if again == nil {
		t.Fatal("再次降级也必须返回可用实例")
	}
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

// TestNewMetrics_NilRegFallsBackToPlatformRegistry 回归测试（#50）：reg 为
// nil 时必须回退到包级 metrics.Registry（平台 /metrics 端点暴露的自定义
// registry，与 metrics.Init 同一处）——此前回退 prom.DefaultRegisterer，
// httpx/nns-endpoint 等弹性栈指标全部落进默认全局 registry，/metrics 抓
// 不到熔断/重试指标，可观测性静默丢失。
func TestNewMetrics_NilRegFallsBackToPlatformRegistry(t *testing.T) {
	m := NewMetrics(nil, "")
	m.ObserveRetry("nil-reg-svc", "httpx")

	// 必须出现在 metrics.Registry（平台抓取面）上，且带正确 series 值。
	mfs, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, mf := range mfs {
		if mf.GetName() != "resilience_retry_attempts_total" {
			continue
		}
		for _, mt := range mf.GetMetric() {
			var service, kind string
			for _, lp := range mt.GetLabel() {
				switch lp.GetName() {
				case "service":
					service = lp.GetValue()
				case "kind":
					kind = lp.GetValue()
				}
			}
			if service == "nil-reg-svc" && kind == "httpx" {
				found = true
				if mt.GetCounter().GetValue() != 1 {
					t.Fatalf("resilience_retry_attempts_total{nil-reg-svc,httpx} = %v, want 1", mt.GetCounter().GetValue())
				}
			}
		}
	}
	if !found {
		t.Fatal("reg=nil 时指标未注册到 metrics.Registry——平台 /metrics 抓不到弹性指标（#50）")
	}

	// 不得再进默认全局 registry（修复前的落点）。
	defMfs, err := prom.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range defMfs {
		if strings.HasPrefix(mf.GetName(), "resilience_") {
			t.Fatalf("弹性指标不应注册进 prom.DefaultRegisterer，发现 %s", mf.GetName())
		}
	}
}
