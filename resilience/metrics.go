// Package resilience 提供客户端弹性设施（熔断器/重试）的共享 Prometheus
// 指标集，供 network/httpx 与 nns/endpoint 等弹性栈消费方复用，保证
// 全库熔断/重试可观测性的指标命名与标签基数规范一致（PLATFORM_CONTRACT §5.5）。
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

	prom "github.com/prometheus/client_golang/prometheus"
)

// 熔断器状态常量（与 gobreaker.State 数值一致，独立定义避免包级依赖）。
const (
	StateClosed   = 0
	StateHalfOpen = 1
	StateOpen     = 2
)

// StateName 状态数值 → label 值。
func StateName(state int) string {
	switch state {
	case StateHalfOpen:
		return "half_open"
	case StateOpen:
		return "open"
	default:
		return "closed"
	}
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

// ObserveTransition 记录一次状态迁移（同时刷新当前状态 gauge）。
func (m *Metrics) ObserveTransition(service, kind string, from, to int) {
	m.breakerTransitions.WithLabelValues(service, kind, StateName(from), StateName(to)).Inc()
	m.ObserveBreakerState(service, kind, to)
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
func registerReuse[T prom.Collector](reg prom.Registerer, fresh T) T {
	if err := reg.Register(fresh); err != nil {
		var are prom.AlreadyRegisteredError
		if errors.As(err, &are) {
			return are.ExistingCollector.(T)
		}
		panic(err)
	}
	return fresh
}
