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

	m.ObserveBreakerStateTyped("svc-a", "httpx", CircuitStateClosed)
	if got := testutil.ToFloat64(m.breakerState.WithLabelValues("svc-a", "", "httpx")); got != 0 {
		t.Fatalf("expected closed state 0, got %v", got)
	}

	m.ObserveTransitionTyped("svc-a", "httpx", CircuitStateClosed, CircuitStateOpen)
	if got := testutil.ToFloat64(m.breakerState.WithLabelValues("svc-a", "", "httpx")); got != 2 {
		t.Fatalf("expected open state 2 after transition, got %v", got)
	}
	if got := testutil.ToFloat64(m.breakerTransitions.WithLabelValues("svc-a", "", "httpx", "closed", "open")); got != 1 {
		t.Fatalf("expected 1 transition, got %v", got)
	}

	m.ObserveRejected("svc-a", "httpx")
	m.ObserveRejected("svc-a", "httpx")
	if got := testutil.ToFloat64(m.breakerRejected.WithLabelValues("svc-a", "", "httpx")); got != 2 {
		t.Fatalf("expected 2 rejections, got %v", got)
	}

	m.ObserveRetry("svc-a", "httpx")
	if got := testutil.ToFloat64(m.retryAttempts.WithLabelValues("svc-a", "", "httpx")); got != 1 {
		t.Fatalf("expected 1 retry, got %v", got)
	}
}

// TestCircuitStateStringTags 验证状态 label 值映射（原 StateName 历史入口删除后，
// 标签值断言由类型化枚举的 String 承接）。
func TestCircuitStateStringTags(t *testing.T) {
	cases := map[CircuitState]string{
		CircuitStateClosed:   "closed",
		CircuitStateHalfOpen: "half_open",
		CircuitStateOpen:     "open",
	}
	for state, want := range cases {
		if got := state.String(); got != want {
			t.Errorf("CircuitState(%d).String() = %q, want %q", int(state), got, want)
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
	if got := testutil.ToFloat64(b.retryAttempts.WithLabelValues("svc", "", "httpx")); got != 1 {
		t.Fatalf("expected shared collector visible across instances, got %v", got)
	}
}

// TestNewMetrics_Defaults 验证 reg/namespace 缺省回退。
func TestNewMetrics_Defaults(t *testing.T) {
	if NewMetrics(nil, "") == nil {
		t.Fatal("expected non-nil metrics with defaults")
	}
}

// —— 序列创建义务（#138）——

// TestNewMetrics_NoInitialSeries_KnownBehavior 钉住已知行为（#138）：
// NewMetrics 只注册 collector、不预写任何 breaker_state 序列——service/kind
// 在首次 Observe* 之前完全缺席。这正是告警盲区的根源（缺席序列不满足
// breaker_state==2 比较、看板空白无法区分「没接入」与「正常关闭」），
// 显式补位由 InitService 承接。
func TestNewMetrics_NoInitialSeries_KnownBehavior(t *testing.T) {
	_, reg := newTestMetrics()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "test_breaker_state" {
			t.Fatalf("NewMetrics 不应预写 breaker_state 序列（已知行为，#138），got %d 条", len(mf.GetMetric()))
		}
	}
}

// TestInitService_CreatesClosedSeries InitService 让序列从创建即存在：
// 写 0=closed（告警/看板对新服务有信号）、后续迁移照常覆盖、本实例重复
// 调用幂等（不把已推进的状态重置回 closed）。
func TestInitService_CreatesClosedSeries(t *testing.T) {
	m, reg := newTestMetrics()
	m.InitService("svc", "httpx")

	want := `# HELP test_breaker_state Circuit breaker state: 0=closed 1=half_open 2=open.
# TYPE test_breaker_state gauge
test_breaker_state{caller="",kind="httpx",service="svc"} 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "test_breaker_state"); err != nil {
		t.Fatalf("InitService 后 breaker_state 应存在且为 0=closed：%v", err)
	}

	// 迁移照常覆盖创建值
	m.ObserveTransitionTyped("svc", "httpx", CircuitStateClosed, CircuitStateOpen)
	if got := testutil.ToFloat64(m.breakerState.WithLabelValues("svc", "", "httpx")); got != 2 {
		t.Fatalf("迁移后 gauge=%v, want 2", got)
	}

	// 本实例重复调用幂等：不重置已推进的状态
	m.InitService("svc", "httpx")
	if got := testutil.ToFloat64(m.breakerState.WithLabelValues("svc", "", "httpx")); got != 2 {
		t.Fatalf("InitService 重复调用不得重置已推进状态，gauge=%v, want 2", got)
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

// —— service/caller 语义分裂（#159）——

// TestLabelConstants 钉住标签名单源常量：消费方按常量构造 PromQL/看板/
// 文档，常量值一旦漂移，查询会静默错配——测试即契约。
func TestLabelConstants(t *testing.T) {
	cases := []struct{ name, got, want string }{
		{"LabelService", LabelService, "service"},
		{"LabelCaller", LabelCaller, "caller"},
		{"LabelKind", LabelKind, "kind"},
		{"LabelFrom", LabelFrom, "from"},
		{"LabelTo", LabelTo, "to"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestMetrics_ServiceIsTarget_CallerIsCaller 钉住 #159 的语义方向：
// service 恒为目标服务、caller 为调用方标识——同一目标下不同调用方是
// 独立序列；既有不传 caller 的调用点落 caller=""（PromQL 中空 label 与
// 缺席等价，sum by (service, kind) 聚合不受影响）。
func TestMetrics_ServiceIsTarget_CallerIsCaller(t *testing.T) {
	m, reg := newTestMetrics()

	// 同一目标服务 downstream，三个调用方（含未标识的旧调用点）。
	m.ObserveRejected("downstream", "httpx")
	m.ObserveRejected("downstream", "httpx", "svc-a")
	m.ObserveRejected("downstream", "httpx", "svc-b")
	m.ObserveRetry("downstream", "httpx", "svc-a")
	m.ObserveBreakerStateTyped("downstream", "httpx", CircuitStateOpen, "svc-a")
	m.InitService("downstream", "httpx", "svc-b")

	want := `# HELP test_breaker_rejected_total Total number of calls rejected while the circuit breaker was open.
# TYPE test_breaker_rejected_total counter
test_breaker_rejected_total{caller="",kind="httpx",service="downstream"} 1
test_breaker_rejected_total{caller="svc-a",kind="httpx",service="downstream"} 1
test_breaker_rejected_total{caller="svc-b",kind="httpx",service="downstream"} 1
# HELP test_breaker_state Circuit breaker state: 0=closed 1=half_open 2=open.
# TYPE test_breaker_state gauge
test_breaker_state{caller="svc-a",kind="httpx",service="downstream"} 2
test_breaker_state{caller="svc-b",kind="httpx",service="downstream"} 0
# HELP test_retry_attempts_total Total number of retry attempts (excluding the initial attempt).
# TYPE test_retry_attempts_total counter
test_retry_attempts_total{caller="svc-a",kind="httpx",service="downstream"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want),
		"test_breaker_rejected_total", "test_breaker_state", "test_retry_attempts_total"); err != nil {
		t.Fatalf("service 必须是目标服务、caller 必须是调用方（#159）：%v", err)
	}
}

// TestMetrics_CallerAtMostOne 多个 caller 违反「调用方标识单值」契约，是
// 编程错误：所有入口共用同一归一化，fail-fast panic 而不是静默丢参数
// （静默丢会把打点落到错误的 caller 序列上）。
func TestMetrics_CallerAtMostOne(t *testing.T) {
	m, _ := newTestMetrics()
	calls := map[string]func(){
		"InitService": func() {
			m.InitService("svc", "httpx", "a", "b")
		},
		"ObserveBreakerStateTyped": func() {
			m.ObserveBreakerStateTyped("svc", "httpx", CircuitStateClosed, "a", "b")
		},
		"ObserveTransitionTyped": func() {
			m.ObserveTransitionTyped("svc", "httpx", CircuitStateClosed, CircuitStateOpen, "a", "b")
		},
		"ObserveRejected": func() { m.ObserveRejected("svc", "httpx", "a", "b") },
		"ObserveRetry":    func() { m.ObserveRetry("svc", "httpx", "a", "b") },
	}
	for name, fn := range calls {
		mustPanic(t, name+" 传两个 caller", fn)
	}
}
