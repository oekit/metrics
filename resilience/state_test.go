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

// TestCircuitStateFromInt_IllegalFallsLoud 验证非法数值不再静默归 closed
// （#66）——closed 是「健康」标签，非法值伪装成它即指标说谎（#39 同族）；
// 本入口是历史 shim（签名无 error），非法值降级为 fail-loud：归 open
// （宁可假警报，不谎报健康）。WARN 可观测单独断言
// （TestCircuitStateFromInt_WarnsOnIllegal）。
func TestCircuitStateFromInt_IllegalFallsLoud(t *testing.T) {
	for _, bad := range []int{-1, 3, 99} {
		if got := CircuitStateFromInt(bad); got != CircuitStateOpen {
			t.Fatalf("CircuitStateFromInt(%d)=%v, want open（fail-loud 兜底，#66）", bad, got)
		}
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

// TestStateName_IllegalMatchesFromInt 验证 StateName 与 CircuitStateFromInt
// 在非法数值上不分叉（StateName 委托实现，钉住不被改回独立 switch）。
func TestStateName_IllegalMatchesFromInt(t *testing.T) {
	for _, bad := range []int{-1, 3, 99} {
		if got, want := StateName(bad), CircuitStateFromInt(bad).String(); got != want {
			t.Fatalf("StateName(%d)=%q，CircuitStateFromInt(%d).String()=%q，两者必须一致",
				bad, got, bad, want)
		}
		if got := StateName(bad); got != "open" {
			t.Fatalf("StateName(%d)=%q, want \"open\"（fail-loud，#66）", bad, got)
		}
	}
}

// TestObserveTransition_IllegalIntFallsLoud 验证历史 int 入口对非法数值的
// 端到端产出（#66）：to 标签 "open"、gauge 置 2（= CircuitStateOpen）——
// 告警面拿到的是「响」而非「谎报健康」。
func TestObserveTransition_IllegalIntFallsLoud(t *testing.T) {
	m, reg := newTestMetrics()
	m.ObserveTransition("svc", "httpx", 0, 7) // 7 非法 → open
	want := `# HELP test_breaker_state Circuit breaker state: 0=closed 1=half_open 2=open.
# TYPE test_breaker_state gauge
test_breaker_state{kind="httpx",service="svc"} 2
# HELP test_breaker_transitions_total Total number of circuit breaker state transitions.
# TYPE test_breaker_transitions_total counter
test_breaker_transitions_total{from="closed",kind="httpx",service="svc",to="open"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want),
		"test_breaker_state", "test_breaker_transitions_total"); err != nil {
		t.Fatalf("非法 int 迁移的 series 不符（#66 fail-loud 口径）：%v", err)
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

// TestCircuitStateFromInt_WarnsOnIllegal 验证非法值的 stderr WARN 可观测
// （#66 修法要求：错标必须可观测，同 registerReuse 降级 WARN 先例）。
func TestCircuitStateFromInt_WarnsOnIllegal(t *testing.T) {
	out := captureStderr(t, func() {
		_ = CircuitStateFromInt(7)
	})
	if !strings.Contains(out, "7") || !strings.Contains(out, "WARN") {
		t.Fatalf("stderr WARN 必须包含非法值 7 与 WARN 标记，got: %q", out)
	}

	out = captureStderr(t, func() {
		_ = CircuitState(99).String()
	})
	if !strings.Contains(out, "99") || !strings.Contains(out, "WARN") {
		t.Fatalf("String() 非法值的 stderr WARN 必须包含 99 与 WARN 标记，got: %q", out)
	}

	// 合法值绝不产生 WARN（避免热路径噪音）。
	out = captureStderr(t, func() {
		for _, s := range []int{0, 1, 2} {
			_ = CircuitStateFromInt(s)
			_ = CircuitState(s).String()
		}
	})
	if out != "" {
		t.Fatalf("合法值不得产生 stderr WARN，got: %q", out)
	}
}
