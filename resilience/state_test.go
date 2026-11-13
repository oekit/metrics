package resilience

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// stateCase 描述一组状态迁移的输入与期望产出。
type stateCase struct {
	name     string
	from     int
	to       int
	wantFrom string
	wantTo   string
}

// stateFixture 覆盖 gobreaker 三态 × resilience 三态共 9 种组合。
//
// gobreaker 数值：StateClosed=0 / StateHalfOpen=1 / StateOpen=2——resilience 的
// CircuitState 与之数值一致，由消费侧（httpx）的编译期钉子保证；本包不依赖
// gobreaker，故此处以数值 fixture 表达同一组状态。
//
// 期望标签是**字符串** closed / half_open / open——不是数字。断言用
// GatherAndCompare 直接比对 Prometheus 暴露文本，数字标签会立刻不匹配。
var stateFixture = []stateCase{
	{"closed→closed", 0, 0, "closed", "closed"},
	{"closed→half_open", 0, 1, "closed", "half_open"},
	{"closed→open", 0, 2, "closed", "open"},
	{"half_open→closed", 1, 0, "half_open", "closed"},
	{"half_open→half_open", 1, 1, "half_open", "half_open"},
	{"half_open→open", 1, 2, "half_open", "open"},
	{"open→closed", 2, 0, "open", "closed"},
	{"open→half_open", 2, 1, "open", "half_open"},
	{"open→open", 2, 2, "open", "open"},
}

// wantExposition 构造期望的 Prometheus 暴露文本（namespace 为测试用 "test"）。
//
// 关键点：from/to 标签值是 "closed"/"half_open"/"open" 三个字符串，
// 且 gauge 的数值是目标态数值（0/1/2）——两者不可混淆。
// 标签在暴露文本中按字母序排列（from < kind < service < to）。
func wantExposition(fx stateCase) string {
	return fmt.Sprintf(`# HELP test_breaker_state Circuit breaker state: 0=closed 1=half_open 2=open.
# TYPE test_breaker_state gauge
test_breaker_state{kind="httpx",service="svc"} %d
# HELP test_breaker_transitions_total Total number of circuit breaker state transitions.
# TYPE test_breaker_transitions_total counter
test_breaker_transitions_total{from="%s",kind="httpx",service="svc",to="%s"} 1
`, fx.to, fx.wantFrom, fx.wantTo)
}

// TestStateMapping 验证类型化入口在 9 种状态组合下写出正确的字符串标签。
func TestStateMapping(t *testing.T) {
	for _, fx := range stateFixture {
		t.Run(fx.name, func(t *testing.T) {
			m, reg := newTestMetrics()
			from, err := CircuitStateFromIntChecked(fx.from)
			if err != nil {
				t.Fatalf("fixture from=%d 非法：%v", fx.from, err)
			}
			to, err := CircuitStateFromIntChecked(fx.to)
			if err != nil {
				t.Fatalf("fixture to=%d 非法：%v", fx.to, err)
			}
			m.ObserveTransitionTyped("svc", "httpx", from, to)
			if err := testutil.GatherAndCompare(reg,
				strings.NewReader(wantExposition(fx)),
				"test_breaker_state", "test_breaker_transitions_total"); err != nil {
				t.Fatalf("类型化入口产出的 series 不符：%v", err)
			}
		})
	}
}

// TestCircuitStateFromIntChecked 表驱动：合法三态零误差映射；非法数值返回
// ErrInvalidState 包装错误（errors.Is 可探测）且零值状态不可误用——与
// CircuitStateFromBreakerValue 同一哲学（#66）。
func TestCircuitStateFromIntChecked(t *testing.T) {
	cases := []struct {
		name    string
		in      int
		want    CircuitState
		wantErr bool
	}{
		{"closed", 0, CircuitStateClosed, false},
		{"half_open", 1, CircuitStateHalfOpen, false},
		{"open", 2, CircuitStateOpen, false},
		{"negative", -1, 0, true},
		{"beyond domain", 3, 0, true},
		{"garbage", 99, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := CircuitStateFromIntChecked(c.in)
			if c.wantErr {
				if !errors.Is(err, ErrInvalidState) {
					t.Fatalf("CircuitStateFromIntChecked(%d) err=%v, want ErrInvalidState", c.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("CircuitStateFromIntChecked(%d) unexpected err: %v", c.in, err)
			}
			if got != c.want {
				t.Fatalf("CircuitStateFromIntChecked(%d)=%v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestCircuitStateString_IllegalFallsLoud 验证非法类型化取值的 String 不再
// 静默归 "closed"（#66 同病入口）——fail-loud 归 "open"。
func TestCircuitStateString_IllegalFallsLoud(t *testing.T) {
	for _, bad := range []CircuitState{CircuitState(-1), CircuitState(3), CircuitState(99)} {
		if got := bad.String(); got != "open" {
			t.Fatalf("CircuitState(%d).String()=%q, want \"open\"（fail-loud，#66）", int(bad), got)
		}
	}
}

// TestObserveBreakerState_TypedMatchesGaugeHelp 验证类型化 gauge 入口三态
// 写出的数值与 help 文本约定一致（0=closed 1=half_open 2=open，#69）。
func TestObserveBreakerState_TypedMatchesGaugeHelp(t *testing.T) {
	cases := []struct {
		name  string
		state CircuitState
		want  float64
	}{
		{"closed", CircuitStateClosed, 0},
		{"half_open", CircuitStateHalfOpen, 1},
		{"open", CircuitStateOpen, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, _ := newTestMetrics()
			m.ObserveBreakerStateTyped("svc", "httpx", c.state)
			if got := testutil.ToFloat64(m.breakerState.WithLabelValues("svc", "httpx")); got != c.want {
				t.Fatalf("ObserveBreakerStateTyped(%v) gauge=%v, want %v", c.state, got, c.want)
			}
		})
	}
}

// TestObserveTransitionTyped_RefreshesGaugeWithoutWarn 钉住内部链路：类型化
// 迁移入口刷 gauge 走 ObserveBreakerStateTyped，合法状态下零 WARN、数值正确。
func TestObserveTransitionTyped_RefreshesGaugeWithoutWarn(t *testing.T) {
	m, _ := newTestMetrics()
	out := captureStderr(t, func() {
		m.ObserveTransitionTyped("svc", "httpx", CircuitStateClosed, CircuitStateOpen)
	})
	if got := testutil.ToFloat64(m.breakerState.WithLabelValues("svc", "httpx")); got != 2 {
		t.Fatalf("ObserveTransitionTyped 后 gauge=%v, want 2", got)
	}
	if out != "" {
		t.Fatalf("类型化迁移入口不得产生 stderr WARN，got: %q", out)
	}
}

// captureStderr 换入临时文件的 os.Stderr 执行 fn，返回期间写出的全部 stderr
// 文本（fmt.Fprintf(os.Stderr,…) 在调用时取包级变量，换入即生效）。
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	saved := os.Stderr
	f, err := os.CreateTemp(t.TempDir(), "warn")
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = f
	defer func() {
		os.Stderr = saved
		_ = f.Close()
	}()
	fn()
	data, readErr := os.ReadFile(f.Name())
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(data)
}

// TestCircuitStateString_IsPure_NoWarn String 为纯函数（#104）：非法值重复
// 调用不产生任何 stderr 输出。Stringer 会被 %v/panic dump/pprof 标签/调试器
// 间接调用——副作用告警在非法值循环场景逐事件刷屏且无法关闭；fail-loud 已
// 收敛到显式转换/打点入口（CircuitStateFromInt / ObserveBreakerStateTyped /
// ObserveTransitionTyped，各自 WARN 语义不变）。标签兜底值 "open" 不变。
func TestCircuitStateString_IsPure_NoWarn(t *testing.T) {
	out := captureStderr(t, func() {
		for range 1000 {
			if got := CircuitState(99).String(); got != "open" {
				t.Fatalf("CircuitState(99).String()=%q, want \"open\"", got)
			}
		}
	})
	if out != "" {
		t.Fatalf("String() 必须是纯函数（#104），却产生 stderr 输出：%q", out)
	}
}

// TestObserveTransitionTyped_InvalidFromWarns 显式打点入口承接 fail-loud
// （#104）：from 非法时本入口打一行 WARN，counter 的 from 标签仍按 "open"
// 兜底（宁可假警报，不谎报健康）；to 合法时无额外 WARN。
func TestObserveTransitionTyped_InvalidFromWarns(t *testing.T) {
	m, reg := newTestMetrics()
	var out string
	out = captureStderr(t, func() {
		m.ObserveTransitionTyped("svc", "httpx", CircuitState(99), CircuitStateClosed)
	})
	if n := strings.Count(out, "[metrics] WARN:"); n != 1 {
		t.Fatalf("非法 from 应告警恰好一次，got %d 次：%q", n, out)
	}
	if !strings.Contains(out, "非法 from 状态值 99") {
		t.Fatalf("WARN 未指明非法 from 值：%q", out)
	}
	want := `# HELP test_breaker_state Circuit breaker state: 0=closed 1=half_open 2=open.
# TYPE test_breaker_state gauge
test_breaker_state{kind="httpx",service="svc"} 0
# HELP test_breaker_transitions_total Total number of circuit breaker state transitions.
# TYPE test_breaker_transitions_total counter
test_breaker_transitions_total{from="open",kind="httpx",service="svc",to="closed"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want),
		"test_breaker_state", "test_breaker_transitions_total"); err != nil {
		t.Fatalf("非法 from 的 counter 标签应按 open 兜底：%v", err)
	}

	// 合法迁移零告警（既有用例 TestObserveTransitionTyped_RefreshesGaugeWithoutWarn
	// 的口径在 #104 之后保持不变）
	out = captureStderr(t, func() {
		m.ObserveTransitionTyped("svc", "httpx", CircuitStateClosed, CircuitStateOpen)
	})
	if out != "" {
		t.Fatalf("合法迁移不得产生 WARN：%q", out)
	}
}
