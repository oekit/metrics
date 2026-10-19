package resilience

import (
	"fmt"
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
			m.ObserveTransitionTyped("svc", "httpx",
				CircuitStateFromInt(fx.from), CircuitStateFromInt(fx.to))
			if err := testutil.GatherAndCompare(reg,
				strings.NewReader(wantExposition(fx)),
				"test_breaker_state", "test_breaker_transitions_total"); err != nil {
				t.Fatalf("类型化入口产出的 series 不符：%v", err)
			}
		})
	}
}

// TestObserveTransition_LegacyShimMatchesTyped 验证历史 int 入口（kitex 等
// 下游仍在用）与类型化入口产出**完全一致**的 series——向后兼容的判据是
// 「旧调用点迁移前后指标不变」，不是「旧入口还在」。
func TestObserveTransition_LegacyShimMatchesTyped(t *testing.T) {
	for _, fx := range stateFixture {
		t.Run(fx.name, func(t *testing.T) {
			m, reg := newTestMetrics()
			// 历史入口：裸 int 形态（向后兼容路径）
			m.ObserveTransition("svc", "httpx", fx.from, fx.to)
			if err := testutil.GatherAndCompare(reg,
				strings.NewReader(wantExposition(fx)),
				"test_breaker_state", "test_breaker_transitions_total"); err != nil {
				t.Fatalf("历史 int 入口产出的 series 与类型化入口不一致：%v", err)
			}
		})
	}
}

// TestStateConstants_AgreeWithTypedEnum 固化「历史 int 常量 == 类型化枚举」。
// 编译期断言已在 metrics.go（数组长度必须为 0），此处补运行期断言以在
// CI 输出里显式呈现该契约。
func TestStateConstants_AgreeWithTypedEnum(t *testing.T) {
	cases := []struct {
		name    string
		legacy  int
		typed   CircuitState
		wantTag string
	}{
		{"closed", StateClosed, CircuitStateClosed, "closed"},
		{"half_open", StateHalfOpen, CircuitStateHalfOpen, "half_open"},
		{"open", StateOpen, CircuitStateOpen, "open"},
	}
	for _, c := range cases {
		if c.legacy != int(c.typed) {
			t.Fatalf("%s: 历史常量=%d 类型化枚举=%d 不一致", c.name, c.legacy, int(c.typed))
		}
		if got := c.typed.String(); got != c.wantTag {
			t.Fatalf("%s: String()=%q, want %q", c.name, got, c.wantTag)
		}
	}
}

// TestStateName_MatchesTypedString 验证两个入口的标签值不分叉。
func TestStateName_MatchesTypedString(t *testing.T) {
	for _, s := range []CircuitState{CircuitStateClosed, CircuitStateHalfOpen, CircuitStateOpen} {
		if got, want := StateName(int(s)), s.String(); got != want {
			t.Fatalf("StateName(%d)=%q，%v.String()=%q，两者必须一致", int(s), got, s, want)
		}
	}
}

// TestCircuitStateFromInt_UnknownFallsBackToClosed 验证非法数值归 closed，
// 不产出不存在的标签值。
func TestCircuitStateFromInt_UnknownFallsBackToClosed(t *testing.T) {
	for _, bad := range []int{-1, 3, 99} {
		if got := CircuitStateFromInt(bad); got != CircuitStateClosed {
			t.Fatalf("CircuitStateFromInt(%d)=%v, want closed", bad, got)
		}
	}
}
