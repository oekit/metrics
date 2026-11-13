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
	first.ObserveBreakerStateTyped("svc", "httpx", CircuitStateHalfOpen)
	second.ObserveBreakerStateTyped("svc", "httpx", CircuitStateOpen)

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
//
// #55：判定「reg==nil 回退平台 Registry」这一契约**不得依赖全局 Registry
// 的初始状态**——进程级全局面在用例/轮次之间没有隔离，registerReuse 在
// Register 撞名时复用既有 collector ⇒ -count=2 第二轮 ObserveRetry 在上一
// 轮留下的同一个 CounterVec 上累加，断言 =1 恒红。本轮把 metrics.Registry
// 换成独立新 registry（用完还原），契约断言落在它上面：回退目标仍必须是
// metrics.Registry 这个「平台抓取面」变量本身。
func TestNewMetrics_NilRegFallsBackToPlatformRegistry(t *testing.T) {
	prevRegistry := metrics.Registry
	metrics.Registry = prom.NewRegistry()
	t.Cleanup(func() { metrics.Registry = prevRegistry })

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

// TestRegisterReuse_TypeMismatch_DegradeInstanceNotRegistered 回归测试（#73）：
// 钉住类型不符降级路径的**真实语义**——Register 已经失败，fresh 从未进入
// Registry、不在任何抓取面上，写降级实例的指标是「指标黑洞」，监控里不会
// 出现第二套序列。此前注释/CHANGELOG 宣称「指标多一套同名不同型的时间
// 序列（Prometheus 注册面仍有旧的那套）」，与实现相悖：运维按「监控里看到
// 两套再修」的预期排查，永远看不到第二套，误以为指标已采集。本测试把
// 更正后的叙事焊死在实现上：降级实例可写（不 panic），但 Gather 面上只有
// 既有 collector，看不到降级实例写出的任何序列；WARN 文案如实说明黑洞。
func TestRegisterReuse_TypeMismatch_DegradeInstanceNotRegistered(t *testing.T) {
	reg := prom.NewRegistry()
	// 同名同描述符，但既有类型是 CounterVec，请求的是 GaugeVec。
	reg.MustRegister(prom.NewCounterVec(prom.CounterOpts{
		Name:      "breaker_state",
		Namespace: "resilience",
		Help:      "Circuit breaker state: 0=closed 1=half_open 2=open.",
	}, []string{"service", "kind"}))

	// stderr WARN 捕获（口径同 TestRegisterReuse_TypeMismatch_DegradesObservably）
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
	var warnBuf strings.Builder
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		warnBuf.WriteString(sc.Text())
	}

	if got == nil {
		t.Fatal("降级路径必须返回可用实例")
	}
	// 降级实例可写（调用方继续跑，不 panic）
	got.WithLabelValues("svc-b", "kind-b").Set(7)

	// 但写入不进任何抓取面（#73）：Gather 面上不得出现降级实例的任何序列
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		for _, mt := range mf.GetMetric() {
			for _, lp := range mt.GetLabel() {
				if lp.GetName() == "service" && lp.GetValue() == "svc-b" {
					t.Fatalf("降级实例的写入出现在抓取面上——降级实例应未注册（指标黑洞，#73）: %s", mf.GetName())
				}
			}
		}
	}

	// WARN 文案如实描述黑洞（#73 更正后的叙事）
	warn := warnBuf.String()
	if !strings.Contains(warn, "未注册") || !strings.Contains(warn, "指标黑洞") {
		t.Fatalf("WARN 应如实说明降级实例未注册/指标黑洞: %s", warn)
	}
}
