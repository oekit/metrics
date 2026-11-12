package metrics

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/oekit/metrics/internal/warn"

	"github.com/prometheus/client_golang/prometheus"
)

// 本文件是「注册复用」单源（#100）：矩阵内此前有三份同款泛型逐字重复——
// httpx mustRegisterReuse、middleware registerReuse（must 面：撞名复用、
// 异型人话 panic）与本仓根包 registerBuildInfo/registerStartTime（容忍面：
// 撞名复用/异型降级 WARN）。must 面收敛为导出的 RegisterReuse 供
// httpx/middleware 等消费方切换；容忍面收敛为内部的 registerTolerant。

// RegisterReuseOption 是 RegisterReuse 的函数选项。
type RegisterReuseOption func(*registerReuseConfig)

type registerReuseConfig struct {
	owner string
}

// WithReuseOwner 自定义同名异型 panic 文案的归属前缀（如 "httpx"、"mw"），
// 让 panic 一眼定位到冲突来源的仓；空串视为未设置，回退默认前缀 "metrics"。
func WithReuseOwner(owner string) RegisterReuseOption {
	return func(c *registerReuseConfig) {
		if owner != "" {
			c.owner = owner
		}
	}
}

// RegisterReuse 注册 collector，同名 collector 已注册时**复用既有实例**
// （抑制重复注册），其余注册错误 panic——prometheus.MustRegister 语义的
// 撞名容忍变体，httpx mustRegisterReuse / middleware registerReuse 的
// 全矩阵单源（#100）。
//
// # 语义
//
//   - 首次注册 → 原样返回 fresh；
//   - 同名重复注册（同一 Registry + namespace 下构造多个 Client/多套中间件
//     的真实场景）→ 复用既有实例返回——**必须复用**：吞掉错误却把新建的
//     （从未注册进 Registry 的）fresh 留在字段里，第二个实例的指标全部
//     丢失且构造"成功"，故障完全静默；
//   - 同名同描述符（fqName + help + 标签名一致）但**类型不同** → prometheus
//     返回 AlreadyRegisteredError 而非直接拒绝，既有 collector 无法当作请求
//     的类型复用。直接 .(T) 会 panic 成一句不可定位的 "interface conversion:
//     ..."，这里换成带归属前缀的人话 panic（既有类型 vs 请求类型 + 怎么改）；
//   - 其余注册错误 → panic 原样上抛（与 MustRegister 边界一致）。
//
// # 用法
//
// 默认前缀 "metrics"：
//
//	gauge := metrics.RegisterReuse(reg, prometheus.NewGaugeVec(opts, labels))
//
// 消费方自带归属前缀（httpx / middleware wave 切换示例，panic 文案与各自
// 历史实现逐字节一致）：
//
//	m.requestsTotal = metrics.RegisterReuse(reg, vec, metrics.WithReuseOwner("httpx"))
//	m.RequestsTotal = metrics.RegisterReuse(reg, vec, metrics.WithReuseOwner("mw"))
//
// 泛型约束 T prometheus.Collector：T 可为具体指针类型（*prometheus.GaugeVec
// 等）或接口类型（prometheus.Gauge 等），复用既有实例经类型断言还原为 T，
// 断言失败即走异型 panic 分支。
//
// 需要撞名**降级**（异型不 panic、WARN 可观测）的容忍语义是另一族：本仓根包
// Init 的 registerTolerant 与 resilience 子包的 registerReuse——不要用本函数
// 承载容忍场景，语义边界见各处 godoc。
//
// reg 为 nil 时回退到包级 metrics.Registry（#109，「nil 参数 = 显式回退默认值」
// 总则②）——与姊妹 API resilience.NewMetrics 的 nil 回退同一处（平台 /metrics
// 端点暴露的自定义 registry）。此前 nil 直接对 nil 接口调用 Register ⇒ SIGSEGV
// panic：既不是回退默认值，也不是构造期 fail-fast，而是第一次使用时的裸崩。
func RegisterReuse[T prometheus.Collector](reg prometheus.Registerer, fresh T, opts ...RegisterReuseOption) T {
	if reg == nil {
		reg = Registry
	}
	if err := reg.Register(fresh); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			existing, ok := are.ExistingCollector.(T)
			if !ok {
				cfg := registerReuseConfig{owner: "metrics"}
				for _, opt := range opts {
					opt(&cfg)
				}
				panic(fmt.Errorf("%s: 同名 collector 类型不符：已注册 %T，请求 %T"+
					"（fqName 与描述符相同但类型不同——请改用同一类型的同名指标，或换 namespace）",
					cfg.owner, are.ExistingCollector, fresh))
			}
			return existing
		}
		panic(err)
	}
	return fresh
}

// warnBlackHoleTail 撞名降级文案的固定尾巴（#101 收敛为常量防漂移）：
// 导出变量不在抓取面上、直接写它是指标黑洞——#73 更正叙事的承诺措辞。
const warnBlackHoleTail = "；导出变量 %s 不在抓取面上，直接写它是指标黑洞"

// registerTolerant 是根包 Init 撞名容忍注册的共享泛型内核（#99：此前
// registerBuildInfo/registerStartTime 是 25 行复制粘贴，仅类型/指标名/
// 导出变量名不同）。语义（#90 撞名容忍，与 resilience.registerReuse 同一
// 哲学）：
//   - 首次注册 → 返回 fresh，error 为 nil；
//   - 既有实例就是 fresh 本身（消费方预注册导出变量的合法路径）→ 幂等返回，
//     不打 WARN，初始值照常写（写的就是抓取面上的同一实例）；
//   - 同名同描述符的其它同型 collector → 复用既有实例返回（app_build_info
//     不缺席），stderr WARN 指明导出变量不在抓取面——后续直接写导出变量
//     是指标黑洞（#73 叙事）；
//   - 异型（同名不同类型）→ 降级 stderr WARN，返回零值 T 与 nil error：
//     写值侧判空跳过，序列以既有实现供给；
//   - 其余错误（同名不同形状等组合态错误）原样上抛，交由 initOnce 的
//     panic 契约处置。
//
// name 为指标名（文案定位用，如 "app_build_info"）；exportVar 为导出变量
// 全名（黑洞文案用，如 "metrics.BuildInfo"）。异型文案的「请求类型」经
// reflect.TypeFor[T]() 从类型参数推导（Go 1.22+），不再随调用点手拼字符串。
func registerTolerant[T prometheus.Collector](reg prometheus.Registerer, fresh T, name, exportVar string) (T, error) {
	if err := reg.Register(fresh); err != nil {
		var are prometheus.AlreadyRegisteredError
		if !errors.As(err, &are) {
			var zero T
			return zero, err
		}
		if any(are.ExistingCollector) == any(fresh) {
			return fresh, nil
		}
		existing, ok := are.ExistingCollector.(T)
		if !ok {
			warn.Warnf("%s 撞名异型（已注册 %T，请求 %s），降级跳过：Init 不写初始值，%s 序列以既有实现供给"+
				warnBlackHoleTail,
				name, are.ExistingCollector, reflect.TypeFor[T](), name, exportVar)
			var zero T
			return zero, nil
		}
		warn.Warnf("Registry 已有同名同型的 %s（非导出变量本身），Init 复用既有实例写初始值"+
			warnBlackHoleTail,
			name, exportVar)
		return existing, nil
	}
	return fresh, nil
}
