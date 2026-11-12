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
//
// 基数防护（#105）：每个指标 family 的标签组合数有运行期上限（缺省
// DefaultLabelLimit，NewMetrics 的 WithLabelLimit 可调）——超限的新组合在
// 首次写入时 fail-fast panic（fail-fast 而非静默丢弃：静默丢打点同样让
// 指标说谎），高基数误用不会无界增长。
package resilience

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/oekit/metrics"
	"github.com/oekit/metrics/internal/warn"
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
//
// 与 concurrency/breaker 互转一律走本包官方转换桥：CircuitStateFromBreakerValue /
// BreakerValue（数值序转换）或 CircuitStateFromName（语义名，零裸 int 路径），
// 禁止消费方手写映射 switch 或两机裸 int 直传——数值序不同，直传会静默把
// open 记成 half_open（#39）。
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
//
// 合法取值（三个枚举常量）映射 closed / half_open / open。非法取值（未受
// 构造约束的强转，如 CircuitState(99)）不静默归 "closed"——closed 是
// 「健康」标签，把非法值伪装成它正是 #39 要消灭、#66 复发的「指标说谎」；
// 标签归 "open"（宁可假警报，不谎报健康）。
//
// 本方法为**纯函数**（#104）：不再打印 stderr 告警。Stringer 会被 %v、
// panic dump、pprof 标签、调试器间接调用——String 里做 I/O 副作用，消费方
// 循环传非法态时告警逐事件刷屏且无法关闭。fail-loud 告警收敛到**显式转换/
// 打点入口**：CircuitStateFromInt / ObserveBreakerStateTyped /
// ObserveTransitionTyped（各自对非法值的 WARN 语义不变）。需要 error 的
// 判定走 BreakerValue / CircuitStateFromIntChecked（非法值返回 ErrInvalidState）。
func (s CircuitState) String() string {
	switch s {
	case CircuitStateClosed:
		return "closed"
	case CircuitStateHalfOpen:
		return "half_open"
	case CircuitStateOpen:
		return "open"
	default:
		return "open"
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
// 入参数值序为 gobreaker 序（= 本包枚举序，closed/half-open/open = 0/1/2）。
// concurrency/breaker 序（closed/open/half-open = 0/1/2）的数值**不同序**，
// 请走 CircuitStateFromBreakerValue——走错入口会把 open/half_open 互换（#39）。
//
// 非法数值（不在 0/1/2 内）不再静默归 closed——「指标说谎」的最后一个静默
// 入口：两套状态机数值序错传时熔断器已 open，标签恒 closed，告警面失真
// （#66，#39 同族；同仓 breaker_compat 的转换桥对非法值一直返回
// ErrInvalidState，本历史入口此前是唯一例外）。本入口是历史 shim（签名无
// error，kitex 等下游依赖），非法值降级为 fail-loud：stderr WARN 可观测 +
// 归 CircuitStateOpen（宁可假警报，不谎报健康）；需要 error 在边界直接
// 拒绝的新调用方用 CircuitStateFromIntChecked。
func CircuitStateFromInt(state int) CircuitState {
	cs, err := CircuitStateFromIntChecked(state)
	if err != nil {
		warn.Warnf("CircuitStateFromInt 收到非法状态值 %d，按 open 兜底（fail-loud，#66）：%v",
			state, err)
		return CircuitStateOpen
	}
	return cs
}

// CircuitStateFromIntChecked 是 CircuitStateFromInt 的类型化变体：非法数值
// 返回 ErrInvalidState 包装错误，绝不静默兜底——与 CircuitStateFromBreakerValue /
// CircuitStateFromName 同一哲学（兜底会把非法值伪装成 closed，#39/#66）。
// 入参数值序与 CircuitStateFromInt 相同（gobreaker 序，closed/half-open/open
// = 0/1/2）。
func CircuitStateFromIntChecked(state int) (CircuitState, error) {
	switch state {
	case int(CircuitStateClosed):
		return CircuitStateClosed, nil
	case int(CircuitStateHalfOpen):
		return CircuitStateHalfOpen, nil
	case int(CircuitStateOpen):
		return CircuitStateOpen, nil
	default:
		return 0, fmt.Errorf("%w：gobreaker 序状态数值 %d（合法域 0/1/2）", ErrInvalidState, state)
	}
}

// StateName 状态数值 → label 值。
//
// 历史入口，保留只为向后兼容。实现委托给类型化枚举，
// 保证两个入口产出的标签值不会分叉。
func StateName(state int) string {
	return CircuitStateFromInt(state).String()
}

// DefaultLabelLimit 单 collector family 的标签组合（child）数缺省上限（#105）。
//
// 本族指标的合法标签（service/kind/from/to）都是受控低基数域——真实组合数
// 在十的量级；1024 对任何健康用法都是宽裕上界，同时把「请求 ID/实例地址这类
// 高基数来源被误作标签」的爆炸限制在千级（而非无界增长到内存泄漏 + /metrics
// 爆序列）。
const DefaultLabelLimit = 1024

// MetricsOption 是 NewMetrics 的函数选项（XxxOption 命名，AGENTS 硬规则）。
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	labelLimit int
}

// WithLabelLimit 定制单 collector family 的标签组合（child）数上限（#105）。
// n <= 0 回退缺省 DefaultLabelLimit（nil/零值参数 = 显式回退默认值，总则②）。
//
// 防护口径：每个 family（breaker_state/breaker_transitions/breaker_rejected/
// retry_attempts）独立记账；新标签组合首次写入时若已超限，**fail-fast panic**
// ——高基数来源（实例地址、请求 ID、方法名）作标签会让 prometheus child map
// 永久增长（内存泄漏 + /metrics 爆序列），这是构造/配置层面的错误信号，静默
// 丢弃打点等于「指标说谎」的软柿子（本仓 #39/#66 反复清剿的形态）。panic 消息
// 携带 family、上限与本次组合，按消息改标签来源或用本选项调整上限。
func WithLabelLimit(n int) MetricsOption {
	return func(c *metricsConfig) {
		if n > 0 {
			c.labelLimit = n
		}
	}
}

// cardinalityGuard 标签基数防护（#105）：记录每个指标 family 已写入的标签
// 组合数，新组合将超出上限时 fail-fast panic。记账只覆盖本包 Observe* 打点
// 路径（与 vec 的实际 child 一一对应：全部写入都经 WithLabelValues 守卫）。
type cardinalityGuard struct {
	limit int
	mu    sync.Mutex
	seen  map[string]map[string]struct{}
}

func newCardinalityGuard(limit int) *cardinalityGuard {
	return &cardinalityGuard{limit: limit, seen: make(map[string]map[string]struct{})}
}

// allow 判定 family+labels 组合可否写入：已见组合放行（热路径一次 map 读）；
// 新组合在限额内记账放行；超限 panic（fail-fast，上限经 WithLabelLimit 调整）。
func (g *cardinalityGuard) allow(family string, labels ...string) {
	key := strings.Join(labels, "\x00")
	g.mu.Lock()
	defer g.mu.Unlock()
	set := g.seen[family]
	if set == nil {
		set = make(map[string]struct{})
		g.seen[family] = set
	}
	if _, ok := set[key]; ok {
		return
	}
	if len(set) >= g.limit {
		panic(fmt.Sprintf("resilience: 指标 %s 的标签组合数达到上限 %d，拒绝新增组合 %q——"+
			"高基数来源（实例地址/请求 ID/方法名等）禁止作标签（包文档「标签约定」节）；"+
			"确需更高上限用 resilience.WithLabelLimit 显式调整（#105）", family, g.limit, key))
	}
	set[key] = struct{}{}
}

// Metrics 弹性指标集。
//
//	{ns}_breaker_state{service,kind}                        Gauge   当前熔断状态（0 closed/1 half-open/2 open）
//	{ns}_breaker_transitions_total{service,kind,from,to}    Counter 状态迁移计数
//	{ns}_breaker_rejected_total{service,kind}               Counter 熔断打开期间被拒绝的调用数
//	{ns}_retry_attempts_total{service,kind}                 Counter 额外重试尝试计数（不含首次）
//
// 标签基数防护（#105）：每个 family 的标签组合数有上限（缺省
// DefaultLabelLimit，WithLabelLimit 可调），超限的新组合在首次写入时
// fail-fast panic——高基数标签来源的误用不会静默无界增长。
type Metrics struct {
	breakerState       *prom.GaugeVec
	breakerTransitions *prom.CounterVec
	breakerRejected    *prom.CounterVec
	retryAttempts      *prom.CounterVec
	guard              *cardinalityGuard
}

// NewMetrics 创建并注册弹性指标集。reg 为 nil 时回退到包级
// metrics.Registry（平台 /metrics 端点暴露的自定义 registry）——与
// metrics.Init 的注册目标同一处，弹性指标才能被平台抓到。此前回退
// prom.DefaultRegisterer，httpx/nns-endpoint 等弹性栈指标全部落进默认
// 全局 registry，/metrics 抓不到熔断/重试指标，可观测性静默丢失（#50）。
// 确需进默认全局 registry 的调用方显式传 prom.DefaultRegisterer。
// namespace 为空时用 "resilience"。同一 Registry + namespace 重复创建时
// 复用已注册的 collector（多 Client/工厂共享指标是常态）。
// opts 定制标签基数防护（WithLabelLimit，#105）。
func NewMetrics(reg prom.Registerer, namespace string, opts ...MetricsOption) *Metrics {
	if reg == nil {
		reg = metrics.Registry
	}
	if namespace == "" {
		namespace = "resilience"
	}
	cfg := metricsConfig{labelLimit: DefaultLabelLimit}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	m := &Metrics{guard: newCardinalityGuard(cfg.labelLimit)}
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

// ObserveBreakerStateTyped 记录熔断器当前状态（创建时置 closed、迁移时置
// 新状态）。
//
// 状态以类型化 CircuitState 传入：数值序与 gobreaker.State 一致
// （Closed=0/HalfOpen=1/Open=2），错位在编译期即被消费侧的钉子挡住，
// 调用方无需（也不应）裸传 int。
//
// 非法取值（未受构造约束的强转，如 CircuitState(99)）不静默原样写入——
// gauge 数值被 help 文本解释为状态档位，写歪即「指标说谎」（#39/#66 同族）。
// 非法值降级为 fail-loud：stderr WARN 可观测 + gauge 归 2（=
// CircuitStateOpen，宁可假警报，不谎报健康，#69）。
func (m *Metrics) ObserveBreakerStateTyped(service, kind string, state CircuitState) {
	cs, err := CircuitStateFromIntChecked(int(state))
	if err != nil {
		warn.Warnf("ObserveBreakerStateTyped 收到非法状态值 %d，gauge 按 open 兜底（fail-loud，#69）：%v",
			int(state), err)
		cs = CircuitStateOpen
	}
	m.guard.allow("breaker_state", service, kind)
	m.breakerState.WithLabelValues(service, kind).Set(float64(cs))
}

// ObserveBreakerState 记录熔断器当前状态（创建时置 closed、迁移时置新状态）。
//
// 入参数值序为 gobreaker 序：0=closed / 1=half_open / 2=open（与 gauge help
// 文本一致）。历史 int shim，内部经 CircuitStateFromInt 收敛为类型化入口：
// 此前裸 int 原样写入 gauge、无归一无告警，是 #66 转换桥防线上唯一不设防
// 的入口——两套状态机数值序不同（concurrency/breaker 为 closed/open/
// half-open=0/1/2），错序的合法值（如 breaker 序 open=1）写进来会被解读成
// half_open 且连 WARN 都没有（#69）。非法值 fail-loud：stderr WARN +
// gauge 归 2（与 #66 同款）。运行期无法分辨「合法但错序」的 int，彻底消除
// 错位请改用 ObserveBreakerStateTyped。
func (m *Metrics) ObserveBreakerState(service, kind string, state int) {
	m.ObserveBreakerStateTyped(service, kind, CircuitStateFromInt(state))
}

// ObserveTransitionTyped 记录一次状态迁移（同时刷新当前状态 gauge）。
//
// 状态以类型化 CircuitState 传入：标签值由枚举决定，调用方无需（也不应）
// 自己拼字符串或裸传 int——数值错位在编译期即被消费侧的钉子挡住。
//
// 非法端点值的 fail-loud 由本显式入口承担（#104：String 已纯函数化，不再
// 打印）——from 在写 counter 前校验告警，to 由 ObserveBreakerStateTyped 校验；
// 两端的 counter 标签仍按 String 的 "open" 兜底不变（宁可假警报，不谎报健康）。
//
// 时序与历史入口保持一致：先算好两端标签值，再写 counter，最后刷 gauge。
func (m *Metrics) ObserveTransitionTyped(service, kind string, from, to CircuitState) {
	if _, err := CircuitStateFromIntChecked(int(from)); err != nil {
		warn.Warnf("ObserveTransitionTyped 收到非法 from 状态值 %d，counter 标签按 open 兜底（fail-loud，#66/#104）：%v",
			int(from), err)
	}
	m.guard.allow("breaker_transitions", service, kind, from.String(), to.String())
	m.breakerTransitions.WithLabelValues(service, kind, from.String(), to.String()).Inc()
	m.ObserveBreakerStateTyped(service, kind, to)
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
	m.guard.allow("breaker_rejected", service, kind)
	m.breakerRejected.WithLabelValues(service, kind).Inc()
}

// ObserveRetry 记录一次额外重试尝试（不含首次）。
func (m *Metrics) ObserveRetry(service, kind string) {
	m.guard.allow("retry_attempts", service, kind)
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
				// 同名但**类型不同**（#44）：消费方先注册过同名 GaugeVec 再换
				// CounterVec，或两个子系统共享 Registry 撞名。此前 here 是硬断言
				// panic——Registry 是进程级共享面，撞名是运行期组合态而非启动
				// 期配置错误，一个子系统炸死整个进程不成比例。降级为可观测：
				// 新建独立实例照常返回。注意降级实例**从未注册进 Registry**
				// （Register 已经失败，fresh 不在任何抓取面上）：写它的指标是
				// **指标黑洞**，监控里不会出现第二套序列——唯一信号是本条
				// stderr WARN（#73 更正叙事：此前「指标多一套同名不同型的
				// 时间序列、监控里看到两套再修」的说法失实，掩盖了数据丢失面）。
				// 排障请按 WARN 立即改 namespace 或统一类型，恢复正常注册路径。
				// 对齐「其余错误仍 panic」的边界：Register 的其它错误
				// （Registry 不可用等）依旧 panic。
				warn.Warnf("同名 collector 类型不符，降级实例未注册进 Registry（写入不进任何抓取面，该实例指标黑洞）："+
					"已注册 %T，请求 %T——请改用同一类型的同名指标或换 namespace",
					are.ExistingCollector, fresh)
				return fresh
			}
			return existing
		}
		panic(err)
	}
	return fresh
}
