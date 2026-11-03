package resilience

import (
	"errors"
	"fmt"
)

// 熔断状态机跨机转换（#39）。
//
// 矩阵内两套熔断状态机的枚举数值序不同：
//
//	| 语义      | concurrency/breaker.State | resilience.CircuitState（= gobreaker） |
//	| closed    | 0                         | 0                                      |
//	| open      | 1                         | 2                                      |
//	| half-open | 2                         | 1                                      |
//
// 两模块依赖面刻意隔离（concurrency 不依赖 metrics，metrics 不依赖 concurrency），
// 因此本桥不引入跨模块类型依赖，以 breaker.State 的数值域（其底层为 int）与语义名
// 解耦：映射契约固化在本文件，两机互转只经本桥，禁止消费方手写映射 switch
// （octopus toCircuitState 形态）或两机裸 int 直传——数值序不同，直传会静默把
// open 记成 half_open（或反向），指标直接说谎。
//
// 消费侧编译期钉子（钉住 breaker 数值序与本桥一致；谁改序谁的构建立刻失败）：
//
//	var (
//	    _ [0]struct{} = [resilience.BreakerValueClosed - int(breaker.StateClosed)]struct{}{}
//	    _ [0]struct{} = [resilience.BreakerValueOpen - int(breaker.StateOpen)]struct{}{}
//	    _ [0]struct{} = [resilience.BreakerValueHalfOpen - int(breaker.StateHalfOpen)]struct{}{}
//	)

// ErrInvalidState 是跨机状态转换收到非法输入（未知数值 / 未知状态名 / 非法
// CircuitState 取值）时的哨兵错误，判定一律 errors.Is。转换绝不静默兜底——
// 兜底会把「非法值」伪装成「closed」，正是 (#39) 要消灭的静默错位。
var ErrInvalidState = errors.New("resilience: 非法状态值")

// BreakerValue* 是 concurrency/breaker.State 的数值镜像（closed/open/half-open = 0/1/2）。
//
// 注意与本包 CircuitState（closed/half-open/open = 0/1/2）**不同序**。这组常量
// 既是 CircuitStateFromBreakerValue / BreakerValue 的映射锚点，也是消费侧编译期
// 钉子的锚点（见文件头范式）：breaker 数值序一旦调整，消费方构建期即被挡住。
const (
	// BreakerValueClosed = breaker.StateClosed（正常放行）。
	BreakerValueClosed = 0
	// BreakerValueOpen = breaker.StateOpen（熔断打开）。
	BreakerValueOpen = 1
	// BreakerValueHalfOpen = breaker.StateHalfOpen（半开探测）。
	BreakerValueHalfOpen = 2
)

// CircuitStateFromBreakerValue 把 concurrency/breaker.State 的数值（closed/open/
// half-open = 0/1/2）转换为本包 CircuitState（closed/half-open/open = 0/1/2）。
//
// 典型用法（bs 为 breaker.State，只在边界转换一次，之后全程类型化 CircuitState）：
//
//	cs, err := resilience.CircuitStateFromBreakerValue(int(bs))
//
// ⚠️ 入参是 **breaker 数值序**，不是 gobreaker 序：gobreaker 序（closed=0/half=1/
// open=2）与本包枚举同序，请直接用 CircuitStateFromInt / 类型化常量，不要走本桥，
// 否则 1/2 会被互换。非法数值返回 ErrInvalidState 包装错误，绝不静默归 closed。
func CircuitStateFromBreakerValue(v int) (CircuitState, error) {
	switch v {
	case BreakerValueClosed:
		return CircuitStateClosed, nil
	case BreakerValueOpen:
		return CircuitStateOpen, nil
	case BreakerValueHalfOpen:
		return CircuitStateHalfOpen, nil
	default:
		return 0, fmt.Errorf("%w：breaker 状态数值 %d", ErrInvalidState, v)
	}
}

// BreakerValue 返回该状态在 concurrency/breaker.State 数值序下的取值（closed/open/
// half-open = 0/1/2），即 CircuitStateFromBreakerValue 的反向转换。
//
// 返回的是 **breaker 数值序**（open=1、half-open=2），与本包枚举数值（open=2、
// half-open=1）不同——禁止与本包 int 形态入口（ObserveTransition 等，gobreaker 序）
// 混用。非法 CircuitState 取值（如 CircuitState(99)）返回 ErrInvalidState 包装错误。
func (s CircuitState) BreakerValue() (int, error) {
	switch s {
	case CircuitStateClosed:
		return BreakerValueClosed, nil
	case CircuitStateOpen:
		return BreakerValueOpen, nil
	case CircuitStateHalfOpen:
		return BreakerValueHalfOpen, nil
	default:
		return 0, fmt.Errorf("%w：CircuitState 取值 %d", ErrInvalidState, int(s))
	}
}

// CircuitStateFromName 按语义名转换为类型化状态，两机互转的零裸 int 路径
// （breaker.State.String() 的输出可直接喂入）：
//
//	cs, err := resilience.CircuitStateFromName(bs.String())
//
// 接受两种拼写：连字符形态 "half-open"（concurrency/breaker.State.String() 输出）与
// 下划线形态 "half_open"（本包 Prometheus 标签值）。其余名称（含大小写变体）一律
// 返回 ErrInvalidState 包装错误，不做模糊匹配。反向直接用 CircuitState.String()。
func CircuitStateFromName(name string) (CircuitState, error) {
	switch name {
	case "closed":
		return CircuitStateClosed, nil
	case "half-open", "half_open":
		return CircuitStateHalfOpen, nil
	case "open":
		return CircuitStateOpen, nil
	default:
		return 0, fmt.Errorf("%w：状态名 %q", ErrInvalidState, name)
	}
}
