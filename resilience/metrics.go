// Package resilience 提供客户端弹性设施（熔断器/重试）的共享 Prometheus
// 指标集，供 network/httpx 与 nns/endpoint 等弹性栈消费方复用，保证
// 全库熔断/重试可观测性的指标命名与标签基数规范一致（平台契约 §5.5）。
//
// 标签约定：
//   - service: 目标服务名（受控低基数）
//   - kind:    弹性栈标识（如 "httpx" / "nns-http" / "nns-grpc"，固定有限集）
//   - from/to: 熔断状态迁移端点（closed/half_open/open，固定有限集）
//
// 禁止将实例地址、方法名等高基数来源作为标签。
package resilience

import (
	"errors"
	"fmt"

	prom "github.com/prometheus/client_golang/prometheus"
)

// CircuitState 熔断器状态（类型化）。
//
// 数值与 gobreaker.State 一致（Closed=0 / HalfOpen=1 / Open=2）。本包不依赖
// gobreaker——一致性由**消费侧的编译期钉子**保证（如 httpx/options.go：
// `var _ [0]struct{} = [int(resilience.CircuitStateOpen) - int(gobreaker.StateOpen)]struct{}{}`），
// 谁改动这里的数值，httpx / nns-endpoint 等消费方立刻编译失败，
// 而不是运行期把熔断状态标签悄悄记反。
//
// 用类型化值而非裸 int 传递状态的理由：矩阵内存在两套状态机且**数值序不同**
// （gobreaker：Closed=0/HalfOpen=1/Open=2；concurrency/breaker：Closed=0/Open=1/HalfOpen=2），
// 裸 int 在两套之间流转时编译器无法察觉错位。
type CircuitState int

const (
	// CircuitStateClosed 正常放行。
	CircuitStateClosed CircuitState = 0
	// CircuitStateHalfOpen 半开：放行探测流量。
	CircuitStateHalfOpen CircuitState = 1
	// CircuitStateOpen 熔断打开：拒绝请求。
	CircuitStateOpen CircuitState = 2
)

// String 返回该状态写入 Prometheus label 的标签值。
func (s CircuitState) String() string {
	switch s {
	case CircuitStateHalfOpen:
		return "half_open"
	case CircuitStateOpen:
		return "open"
	default:
		return "closed"
	}
}

// 熔断器状态常量（历史 int 入口，与 gobreaker.State 数值一致，独立定义避免包级依赖）。
//
// 历史入口，保留只为向后兼容（kitex 等下游仍在用 resilience.StateOpen /
// resilience.StateName）。新代码请用 CircuitState 与 ObserveTransitionTyped。
//
// 注意：这里**刻意不加机器可读的 `Deprecated:` 标记**——staticcheck 的
// SA1019 会跨模块触发（实测：跨模块调用被弃用符号即报 SA1019），
// 加标记会让仍在使用本组符号的下游仓 CI 变红。迁移意图以本段文字标注。
const (
	StateClosed   = 0
	StateHalfOpen = 1
	StateOpen     = 2
)

// 编译期断言：历史 int 常量与类型化枚举数值必须一致。
//
// 手法是「数组长度必须为 0」——差值非 0 即编译失败（差值为正负都失败，
// 比 uint8(差值) 断言更强：后者只在差值为负时报错）。
// 目的：resilience 自身两套入口不允许数值分叉，否则同一状态经不同入口
// 写出的标签会不一致。
var (
	_ [0]struct{} = [StateClosed - int(CircuitStateClosed)]struct{}{}
	_ [0]struct{} = [StateHalfOpen - int(CircuitStateHalfOpen)]struct{}{}
	_ [0]struct{} = [StateOpen - int(CircuitStateOpen)]struct{}{}
)

// CircuitStateFromInt 把历史 int 状态映射为类型化状态。
//
// 显式三态映射（不依赖数值区间推断）：未知数值一律归 closed，
// 避免把非法值写成不存在的标签值。
func CircuitStateFromInt(state int) CircuitState {
	switch state {
	case int(CircuitStateHalfOpen):
		return CircuitStateHalfOpen
	case int(CircuitStateOpen):
		return CircuitStateOpen
	default:
		return CircuitStateClosed
	}
}

// StateName 状态数值 → label 值。
//
// 历史入口，保留只为向后兼容。实现委托给类型化枚举，
// 保证两个入口产出的标签值不会分叉。
func StateName(state int) string {
	return CircuitStateFromInt(state).String()
}

// Metrics 弹性指标集。
//
//	{ns}_breaker_state{service,kind}                        Gauge   当前熔断状态（0 closed/1 half-open/2 open）
//	{ns}_breaker_transitions_total{service,kind,from,to}    Counter 状态迁移计数
//	{ns}_breaker_rejected_total{service,kind}               Counter 熔断打开期间被拒绝的调用数
//	{ns}_retry_attempts_total{service,kind}                 Counter 额外重试尝试计数（不含首次）
type Metrics struct {
	breakerState       *prom.GaugeVec
	breakerTransitions *prom.CounterVec
	breakerRejected    *prom.CounterVec
	retryAttempts      *prom.CounterVec
}

// NewMetrics 创建并注册弹性指标集。reg 为 nil 时用全局 DefaultRegisterer。
// namespace 为空时用 "resilience"。同一 Registry + namespace 重复创建时
// 复用已注册的 collector（多 Client/工厂共享指标是常态）。
func NewMetrics(reg prom.Registerer, namespace string) *Metrics {
	if reg == nil {
		reg = prom.DefaultRegisterer
	}
	if namespace == "" {
		namespace = "resilience"
	}
	m := &Metrics{}
	m.breakerState = registerReuse(reg, prom.NewGaugeVec(prom.GaugeOpts{
		Name:      "breaker_state",
		Namespace: namespace,
		Help:      "Circuit breaker state: 0=closed 1=half_open 2=open.",
	}, []string{"service", "kind"}))
	m.breakerTransitions = registerReuse(reg, prom.NewCounterVec(prom.CounterOpts{
		Name:      "breaker_transitions_total",
		Namespace: namespace,
		Help:      "Total number of circuit breaker state transitions.",
	}, []string{"service", "kind", "from", "to"}))
	m.breakerRejected = registerReuse(reg, prom.NewCounterVec(prom.CounterOpts{
		Name:      "breaker_rejected_total",
		Namespace: namespace,
		Help:      "Total number of calls rejected while the circuit breaker was open.",
	}, []string{"service", "kind"}))
	m.retryAttempts = registerReuse(reg, prom.NewCounterVec(prom.CounterOpts{
		Name:      "retry_attempts_total",
		Namespace: namespace,
		Help:      "Total number of retry attempts (excluding the initial attempt).",
	}, []string{"service", "kind"}))
	return m
}

// ObserveBreakerState 记录熔断器当前状态（创建时置 closed、迁移时置新状态）。
func (m *Metrics) ObserveBreakerState(service, kind string, state int) {
	m.breakerState.WithLabelValues(service, kind).Set(float64(state))
}

// ObserveTransitionTyped 记录一次状态迁移（同时刷新当前状态 gauge）。
//
// 状态以类型化 CircuitState 传入：标签值由枚举决定，调用方无需（也不应）
// 自己拼字符串或裸传 int——数值错位在编译期即被消费侧的钉子挡住。
//
// 时序与历史入口保持一致：先算好两端标签值，再写 counter，最后刷 gauge。
func (m *Metrics) ObserveTransitionTyped(service, kind string, from, to CircuitState) {
	m.breakerTransitions.WithLabelValues(service, kind, from.String(), to.String()).Inc()
	m.ObserveBreakerState(service, kind, int(to))
}

// ObserveTransition 记录一次状态迁移（同时刷新当前状态 gauge）。
//
// 历史入口，保留只为向后兼容（kitex 等下游仍在用 int 形态）。内部做显式
// 三态映射后转调 ObserveTransitionTyped，两个入口产出的标签值不会分叉。
//
// 新代码请用 ObserveTransitionTyped。
func (m *Metrics) ObserveTransition(service, kind string, from, to int) {
	m.ObserveTransitionTyped(service, kind, CircuitStateFromInt(from), CircuitStateFromInt(to))
}

// ObserveRejected 记录一次熔断打开期的调用拒绝。
func (m *Metrics) ObserveRejected(service, kind string) {
	m.breakerRejected.WithLabelValues(service, kind).Inc()
}

// ObserveRetry 记录一次额外重试尝试（不含首次）。
func (m *Metrics) ObserveRetry(service, kind string) {
	m.retryAttempts.WithLabelValues(service, kind).Inc()
}

// registerReuse 注册 collector，同名已注册时复用既有实例（多 Client/工厂
// 共享同一 Registry + namespace 的常态路径），其余错误仍 panic。
//
// 同名同描述符（fqName + help + 标签名都一致）但**类型不同**时，
// prometheus 返回的是 AlreadyRegisteredError（而非直接拒绝）——此时既有
// collector 无法当作请求的那个类型复用。此前直接 `.(T)` 会 panic 成一句
// 不可定位的 "interface conversion: prometheus.Collector is
// *prometheus.CounterVec, not *prometheus.GaugeVec"；改为换成人能读懂的错误
// （既有类型 vs 请求类型 + 怎么改）。
func registerReuse[T prom.Collector](reg prom.Registerer, fresh T) T {
	if err := reg.Register(fresh); err != nil {
		var are prom.AlreadyRegisteredError
		if errors.As(err, &are) {
			existing, ok := are.ExistingCollector.(T)
			if !ok {
				panic(fmt.Errorf("metrics: 同名 collector 类型不符：已注册 %T，请求 %T"+
					"（fqName 与描述符相同但类型不同——请改用同一类型的同名指标，或换 namespace）",
					are.ExistingCollector, fresh))
			}
			return existing
		}
		panic(err)
	}
	return fresh
}
