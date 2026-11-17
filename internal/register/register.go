// Package register 提供 oekit/metrics 模块内「注册复用」骨架的单源实现（#148）。
//
// must 面（根包导出的 RegisterReuse）与容忍面（根包 Init 的 registerTolerant、
// resilience 子包的 registerReuse）此前各自维护一份同款骨架——Register →
// errors.As AlreadyRegisteredError → 类型断言 → 复用/异型处置 → panic/上抛——
// 三份逐字重复且语义注释各自漂移。本包把骨架收敛为一份泛型内核，分支差异以
// 行为选项表达：
//
//   - 异型处置（同名同描述符但类型不同）：WithForeignPanic（must 面：带归属
//     前缀的人话 error 上抛，壳 panic）/ WithForeignZero（root 容忍面：WARN +
//     返回零值）/ WithForeignFresh（resilience 容忍面：WARN + 返回未注册的
//     fresh，即指标黑洞）。**三者的文档化语义差不可收敛**——零值让写值侧判空
//     跳过（序列以既有实现供给），fresh 让调用方拿到可写但不进抓取面的实例，
//     panic 则与 MustRegister 边界一致，谁也不吞谁的降级。
//   - 复用可观测（WithReuseWarn）：仅 root 容忍面启用——同型**异实例**复用打
//     黑洞叙事 WARN（导出变量不在抓取面），既有实例就是 fresh 本身走静默幂等
//     （消费方预注册导出变量的合法路径）。must/resilience 面的复用是静默常态
//     路径。
//   - 非撞名错误不在容忍面：内核原样上抛（zero, err），处置交壳——must/
//     resilience 壳 panic（与 MustRegister 边界一致），root 壳交给 initOnce
//     的 panic 契约。
//
// 放 internal/ 的理由同 internal/warn（#101 先例）：分支处置与文案是**模块
// 私有约定**，不对外承诺——对外导出面只有根包 metrics.RegisterReuse 一族，
// 容忍面两处都是未导出符号。Go 的未导出符号包级可见，根包与子包无法直接
// 共享，internal/ 是同 module 跨包共享的标准位。
//
// WARN 经 internal/warn 出口（"[metrics] WARN: " 前缀 + 结尾换行的单源）。
// reg 不做 nil 兜底：nil 回退是各壳的文档化契约（RegisterReuse/#109 回退
// metrics.Registry、resilience.NewMetrics/#50 同），内核不越权接管。
package register

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/oekit/metrics/internal/warn"

	"github.com/prometheus/client_golang/prometheus"
)

// blackHoleTail 撞名降级文案的固定尾巴（#101 收敛为常量防漂移，#148 随骨架
// 迁入本包）：导出变量不在抓取面上、直接写它是指标黑洞——#73 更正叙事的
// 承诺措辞。
const blackHoleTail = "；导出变量 %s 不在抓取面上，直接写它是指标黑洞"

// foreignDisposition 异型（同名同描述符但类型不同）的处置策略。
type foreignDisposition int

const (
	// foreignPanic must 面：人话 error 上抛（壳 panic），与 MustRegister 的
	// fail-fast 边界一致。
	foreignPanic foreignDisposition = iota
	// foreignZero root 容忍面：WARN + 返回零值——写值侧判空跳过，序列以
	// 既有实现供给。
	foreignZero
	// foreignFresh resilience 容忍面：WARN + 返回未注册 fresh——Register 已
	// 失败，fresh 不在任何抓取面，写它是指标黑洞（#73）。
	foreignFresh
)

// ReuseOption 是 Reuse 的函数选项（XxxOption 命名，AGENTS 硬规则）。
type ReuseOption func(*config)

type config struct {
	foreign   foreignDisposition
	owner     string // foreignPanic 文案的归属前缀
	name      string // foreignZero/复用 WARN 文案的指标名（撞名定位）
	exportVar string // foreignZero/复用 WARN 文案的导出变量全名（黑洞叙事）
	reuseWarn bool   // 同型异实例复用打 WARN + 同实例静默幂等（root 容忍面）
}

// WithForeignPanic 异型处置为 must 面：同名异型以带归属前缀的人话 error
// 上抛（由壳 panic）。owner 为文案归属前缀（如 "httpx"、"mw"），空串视为
// 未设置，回退默认前缀 "metrics"。
func WithForeignPanic(owner string) ReuseOption {
	return func(c *config) {
		c.foreign = foreignPanic
		if owner != "" {
			c.owner = owner
		}
	}
}

// WithForeignZero 异型处置为 root 容忍面：stderr WARN 后返回零值 T 与 nil
// error——写值侧判空跳过，序列以既有实现供给。name 为指标名（文案定位用，
// 如 "app_build_info"）；exportVar 为导出变量全名（黑洞文案用，如
// "metrics.BuildInfo"）。异型文案的「请求类型」经 reflect.TypeFor[T]() 从
// 类型参数推导（Go 1.22+），不随调用点手拼字符串。
func WithForeignZero(name, exportVar string) ReuseOption {
	return func(c *config) {
		c.foreign = foreignZero
		c.name = name
		c.exportVar = exportVar
	}
}

// WithForeignFresh 异型处置为 resilience 容忍面：stderr WARN 后返回 fresh。
// 注意返回的 fresh **从未注册进 Registry**（Register 已经失败，fresh 不在
// 任何抓取面上）——写它的指标是指标黑洞，唯一信号是那条 stderr WARN（#73
// 更正叙事）；排障按 WARN 改 namespace 或统一类型，恢复正常注册路径。
func WithForeignFresh() ReuseOption {
	return func(c *config) {
		c.foreign = foreignFresh
	}
}

// WithReuseWarn root 容忍面的复用可观测契约（#90/#99）：同名同型的**异实例**
// 复用时打 stderr WARN（导出变量不在抓取面的黑洞叙事，文案需
// WithForeignZero 的 name/exportVar）；既有实例就是 fresh 本身时静默幂等
// 返回、不打 WARN。must/resilience 面的复用是静默常态路径（同实例经类型
// 断言自然复用、返回值与 fresh 相同），不启用本选项。
func WithReuseWarn() ReuseOption {
	return func(c *config) {
		c.reuseWarn = true
	}
}

// Reuse 注册复用骨架的单源实现（#148）：首次注册原样返回 fresh；同名撞名
// 经 errors.As AlreadyRegisteredError 判定后按行为选项处置（异型三策 +
// root 复用可观测）；其余注册错误不在容忍面，原样上抛（zero, err）交由壳
// 处置（must/resilience panic、root 上抛）。
//
// 与三个壳的既有语义逐字节对齐：
//   - RegisterReuse（must，WithForeignPanic）：nil reg 回退、panic 边界见根包
//     godoc（#100/#109）；
//   - registerTolerant（root，WithForeignZero + WithReuseWarn）：#90 撞名容忍
//     契约见根包 godoc；
//   - resilience.registerReuse（resilience，WithForeignFresh）：#44/#73 降级
//     契约见子包 godoc。
func Reuse[T prometheus.Collector](reg prometheus.Registerer, fresh T, opts ...ReuseOption) (T, error) {
	cfg := config{foreign: foreignPanic, owner: "metrics"}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if err := reg.Register(fresh); err != nil {
		var are prometheus.AlreadyRegisteredError
		if !errors.As(err, &are) {
			var zero T
			return zero, err
		}
		existing, ok := are.ExistingCollector.(T)
		if !ok {
			switch cfg.foreign {
			case foreignZero:
				warn.Warnf("%s 撞名异型（已注册 %T，请求 %s），降级跳过：Init 不写初始值，%s 序列以既有实现供给"+
					blackHoleTail,
					cfg.name, are.ExistingCollector, reflect.TypeFor[T](), cfg.name, cfg.exportVar)
				var zero T
				return zero, nil
			case foreignFresh:
				warn.Warnf("同名 collector 类型不符，降级实例未注册进 Registry（写入不进任何抓取面，该实例指标黑洞）："+
					"已注册 %T，请求 %T——请改用同一类型的同名指标或换 namespace",
					are.ExistingCollector, fresh)
				return fresh, nil
			default:
				var zero T
				return zero, fmt.Errorf("%s: 同名 collector 类型不符：已注册 %T，请求 %T"+
					"（fqName 与描述符相同但类型不同——请改用同一类型的同名指标，或换 namespace）",
					cfg.owner, are.ExistingCollector, fresh)
			}
		}
		if cfg.reuseWarn {
			if any(are.ExistingCollector) == any(fresh) {
				return fresh, nil
			}
			warn.Warnf("Registry 已有同名同型的 %s（非导出变量本身），Init 复用既有实例写初始值"+
				blackHoleTail,
				cfg.name, cfg.exportVar)
		}
		return existing, nil
	}
	return fresh, nil
}
