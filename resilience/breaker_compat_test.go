package resilience

import (
	"errors"
	"testing"
)

// breakerCase 描述一组跨机映射的输入与期望产出。
type breakerCase struct {
	breakerValue int
	want         CircuitState
	wantLabel    string
}

// breakerFixture 覆盖 breaker 三态 × resilience 三态的全量映射。
//
// 关键契约（#39）：breaker 数值序是 closed/open/half-open = 0/1/2，本包枚举是
// closed/half-open/open = 0/1/2——open 与 half-open 两侧数值**恰好错位**，
// 映射必须语义对语义，绝不能数值直通。
var breakerFixture = []breakerCase{
	{BreakerValueClosed, CircuitStateClosed, "closed"},
	{BreakerValueOpen, CircuitStateOpen, "open"},
	{BreakerValueHalfOpen, CircuitStateHalfOpen, "half_open"},
}

// TestCircuitStateFromBreakerValue_MapsThreeStates 验证正向转换三态全覆盖，
// 且转换产出的标签值（指标暴露面）与语义一致——指标不再说谎。
func TestCircuitStateFromBreakerValue_MapsThreeStates(t *testing.T) {
	for _, c := range breakerFixture {
		got, err := CircuitStateFromBreakerValue(c.breakerValue)
		if err != nil {
			t.Fatalf("CircuitStateFromBreakerValue(%d) 返回错误：%v", c.breakerValue, err)
		}
		if got != c.want {
			t.Fatalf("CircuitStateFromBreakerValue(%d)=%v, want %v", c.breakerValue, got, c.want)
		}
		if got.String() != c.wantLabel {
			t.Fatalf("CircuitStateFromBreakerValue(%d).String()=%q, want %q",
				c.breakerValue, got.String(), c.wantLabel)
		}
	}
}

// TestCircuitStateFromBreakerValue_OpenHalfNotSwapped 数值序错位回归（#39）：
// breaker 的 open=1 / half-open=2 与本包枚举的 open=2 / half-open=1 恰好错位，
// 裸 int 直传（或误走 gobreaker 序入口）会把 open 静默记成 half_open。本用例
// 钉死语义：breaker 数值 1 必须转出 open、2 必须转出 half_open，且两者不可互换。
func TestCircuitStateFromBreakerValue_OpenHalfNotSwapped(t *testing.T) {
	open, err := CircuitStateFromBreakerValue(BreakerValueOpen) // breaker open = 1
	if err != nil {
		t.Fatalf("CircuitStateFromBreakerValue(%d) 返回错误：%v", BreakerValueOpen, err)
	}
	if open != CircuitStateOpen || open == CircuitStateHalfOpen {
		t.Fatalf("breaker open(=%d) 转出 %v，必须是 open 且不得是 half_open（#39 互换回归）",
			BreakerValueOpen, open)
	}
	half, err := CircuitStateFromBreakerValue(BreakerValueHalfOpen) // breaker half-open = 2
	if err != nil {
		t.Fatalf("CircuitStateFromBreakerValue(%d) 返回错误：%v", BreakerValueHalfOpen, err)
	}
	if half != CircuitStateHalfOpen || half == CircuitStateOpen {
		t.Fatalf("breaker half-open(=%d) 转出 %v，必须是 half_open 且不得是 open（#39 互换回归）",
			BreakerValueHalfOpen, half)
	}
	// 同一数值走 gobreaker 序入口（CircuitStateFromIntChecked）得到的是相反
	// 语义——这就是 (#39) 的静默错位本体，转换桥存在的理由。
	if goOpen, err := CircuitStateFromIntChecked(BreakerValueOpen); err != nil || goOpen == open {
		t.Fatalf("gobreaker 序入口与 breaker 序转换产出同一语义（err=%v），说明数值序假设已失效，请复查映射表", err)
	}
	if goHalf, err := CircuitStateFromIntChecked(BreakerValueHalfOpen); err != nil || goHalf == half {
		t.Fatalf("gobreaker 序入口与 breaker 序转换产出同一语义（err=%v），说明数值序假设已失效，请复查映射表", err)
	}
}

// TestCircuitStateBreakerValue_MapsThreeStates 验证反向转换三态全覆盖：
// closed→0、open→1、half-open→2（breaker 数值序）。
func TestCircuitStateBreakerValue_MapsThreeStates(t *testing.T) {
	for _, c := range breakerFixture {
		got, err := c.want.BreakerValue()
		if err != nil {
			t.Fatalf("%v.BreakerValue() 返回错误：%v", c.want, err)
		}
		if got != c.breakerValue {
			t.Fatalf("%v.BreakerValue()=%d, want %d", c.want, got, c.breakerValue)
		}
	}
}

// TestBreakerValue_RoundTrip 双向往返守恒：三态两侧互转不丢语义。
func TestBreakerValue_RoundTrip(t *testing.T) {
	for _, c := range breakerFixture {
		// breaker 数值 → CircuitState → breaker 数值
		cs, err := CircuitStateFromBreakerValue(c.breakerValue)
		if err != nil {
			t.Fatalf("正向转换(%d) 返回错误：%v", c.breakerValue, err)
		}
		back, err := cs.BreakerValue()
		if err != nil {
			t.Fatalf("反向转换(%v) 返回错误：%v", cs, err)
		}
		if back != c.breakerValue {
			t.Fatalf("round-trip(%d)=%d，往返必须守恒", c.breakerValue, back)
		}
		// CircuitState → breaker 数值 → CircuitState
		bv, err := c.want.BreakerValue()
		if err != nil {
			t.Fatalf("反向转换(%v) 返回错误：%v", c.want, err)
		}
		forth, err := CircuitStateFromBreakerValue(bv)
		if err != nil {
			t.Fatalf("正向转换(%d) 返回错误：%v", bv, err)
		}
		if forth != c.want {
			t.Fatalf("round-trip(%v)=%v，往返必须守恒", c.want, forth)
		}
	}
}

// TestCircuitStateFromBreakerValue_InvalidRejected 非法值防御：未知数值一律
// 返回 ErrInvalidState，绝不静默归 closed（静默兜底会把非法值伪装成 closed）。
func TestCircuitStateFromBreakerValue_InvalidRejected(t *testing.T) {
	for _, bad := range []int{-1, 3, 42, 99} {
		got, err := CircuitStateFromBreakerValue(bad)
		if !errors.Is(err, ErrInvalidState) {
			t.Fatalf("CircuitStateFromBreakerValue(%d) err=%v, want ErrInvalidState", bad, err)
		}
		if got != CircuitStateClosed {
			t.Fatalf("CircuitStateFromBreakerValue(%d) 出错时返回 %v，出错返回值必须是零值", bad, got)
		}
	}
}

// TestCircuitStateBreakerValue_InvalidRejected 非法值防御（反向）：非法
// CircuitState 取值一律返回 ErrInvalidState，不产出看似合法的 breaker 数值。
func TestCircuitStateBreakerValue_InvalidRejected(t *testing.T) {
	for _, bad := range []CircuitState{CircuitState(-1), CircuitState(3), CircuitState(99)} {
		got, err := bad.BreakerValue()
		if !errors.Is(err, ErrInvalidState) {
			t.Fatalf("CircuitState(%d).BreakerValue() err=%v, want ErrInvalidState", int(bad), err)
		}
		if got != BreakerValueClosed {
			t.Fatalf("CircuitState(%d).BreakerValue() 出错时返回 %d，出错返回值必须是零值", int(bad), got)
		}
	}
}

// TestCircuitStateFromName_MapsThreeStates 验证语义名正向转换：breaker.State.String()
// 的三种输出（closed/open/half-open）全部命中正确语义，另兼容本包标签拼写 half_open。
func TestCircuitStateFromName_MapsThreeStates(t *testing.T) {
	cases := []struct {
		name string
		want CircuitState
	}{
		{"closed", CircuitStateClosed},
		{"open", CircuitStateOpen},
		{"half-open", CircuitStateHalfOpen}, // breaker.State.String() 拼写
		{"half_open", CircuitStateHalfOpen}, // 本包 Prometheus 标签拼写
	}
	for _, c := range cases {
		got, err := CircuitStateFromName(c.name)
		if err != nil {
			t.Fatalf("CircuitStateFromName(%q) 返回错误：%v", c.name, err)
		}
		if got != c.want {
			t.Fatalf("CircuitStateFromName(%q)=%v, want %v", c.name, got, c.want)
		}
	}
}

// TestCircuitStateFromName_RoundTrip 经 String()（标签形态）往返守恒——
// FromName 必须接受 String() 的产出。
func TestCircuitStateFromName_RoundTrip(t *testing.T) {
	for _, s := range []CircuitState{CircuitStateClosed, CircuitStateHalfOpen, CircuitStateOpen} {
		got, err := CircuitStateFromName(s.String())
		if err != nil {
			t.Fatalf("CircuitStateFromName(%q) 返回错误：%v", s.String(), err)
		}
		if got != s {
			t.Fatalf("round-trip(%v)=%v，往返必须守恒", s, got)
		}
	}
}

// TestCircuitStateFromName_InvalidRejected 非法值防御：未知名称（含大小写变体）
// 一律返回 ErrInvalidState，不做模糊匹配。
func TestCircuitStateFromName_InvalidRejected(t *testing.T) {
	for _, bad := range []string{"", "opened", "Closed", "HALF_OPEN", "half open", "unknown"} {
		got, err := CircuitStateFromName(bad)
		if !errors.Is(err, ErrInvalidState) {
			t.Fatalf("CircuitStateFromName(%q) err=%v, want ErrInvalidState", bad, err)
		}
		if got != CircuitStateClosed {
			t.Fatalf("CircuitStateFromName(%q) 出错时返回 %v，出错返回值必须是零值", bad, got)
		}
	}
}

// TestBreakerValueConstants_DistinctAndPinned 固化镜像常量表：
// 三值互异、与文档映射表逐字一致（0 closed / 1 open / 2 half-open），
// 且与反向转换产出同一数值——常量、映射、文档三方不分叉。
func TestBreakerValueConstants_DistinctAndPinned(t *testing.T) {
	if BreakerValueClosed != 0 || BreakerValueOpen != 1 || BreakerValueHalfOpen != 2 {
		t.Fatalf("BreakerValue* 镜像常量偏离文档映射表：closed=%d open=%d half-open=%d，want 0/1/2",
			BreakerValueClosed, BreakerValueOpen, BreakerValueHalfOpen)
	}
	if BreakerValueClosed == BreakerValueOpen || BreakerValueOpen == BreakerValueHalfOpen ||
		BreakerValueClosed == BreakerValueHalfOpen {
		t.Fatal("BreakerValue* 三值必须互异")
	}
	for _, c := range breakerFixture {
		got, err := c.want.BreakerValue()
		if err != nil {
			t.Fatalf("%v.BreakerValue() 返回错误：%v", c.want, err)
		}
		if got != c.breakerValue {
			t.Fatalf("%v.BreakerValue()=%d 与镜像常量表 %d 不一致", c.want, got, c.breakerValue)
		}
	}
}
