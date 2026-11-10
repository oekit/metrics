package metrics

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/oekit/core/version"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Registry 是自定义的 Prometheus 注册器。
// 传入 ekit/middleware 的 PrometheusMetrics 时使用，避免全局 Registry 冲突。
var Registry = prometheus.NewRegistry()

var (
	// BuildInfo 暴露构建元数据（commit、branch、build_time、go_version）。
	BuildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "app_build_info",
		Help: "Build information about the service",
	}, []string{"service", "commit", "branch", "build_time", "go_version"})

	// StartTimeSeconds 记录进程启动的 Unix 时间戳。
	StartTimeSeconds = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "app_start_time_seconds",
		Help: "Unix timestamp of service start time",
	})
)

// initOnceBody 把 initMetrics 的非撞名失败升级为 panic（见 initOnce 契约）。
func initOnceBody() {
	if err := initMetrics(); err != nil {
		panic(err)
	}
}

// initOnce 用 sync.OnceFunc 而非 sync.Once（Go 1.21+，#90）：OnceFunc 的
// f panic 后，后续每次调用都会把同一 panic 值再炸出来——Init 因非撞名的
// 注册错误失败时**绝不静默**。对比 sync.Once「panic 即算完成」：重调 Init
// 整体跳过 f，注册半途而废、app_build_info 永久缺失且零线索（#90 实测）。
var initOnce = sync.OnceFunc(initOnceBody)

// currentService 保存 Init 首次调用生效的 service 名（Init 之外只读）。
// 用 atomic 而非裸 string：CurrentService 可能与首次 Init 并发执行（#49）。
var currentService atomic.Pointer[string]

// Init 注册所有通用指标并设置初始值。
// 参数 serviceName 用于 app_build_info 的 service 标签，如 "fuxi_engine"、"moyu-server"。
// 必须在 main.go 中调用，且在启动 Hertz server 之前。
//
// 多次调用是安全的，只有第一次调用会注册指标；service 标签也取**第一次**
// 调用传入的名字——进程内 service 名首调即定，后续调用传入不同的
// serviceName 不会生效（打 stderr WARN 提示，不再静默丢弃，#49），
// 如需更名请重启进程。当前生效值可用 CurrentService() 查询。
//
// # 撞名容忍（#90）
//
// 消费方在 Init 之前把导出变量（BuildInfo/StartTimeSeconds）或同名指标
// 自行注册进 Registry 是运行期组合态而非启动期配置错误（与
// resilience.registerReuse 同一哲学），不再 panic：
//   - 同一实例已在 Registry 上 → 幂等跳过，初始值照常写（写的就是
//     抓取面上的同一实例）；
//   - 同名同描述符的其它 collector → 复用既有实例：BuildInfo/
//     StartTimeSeconds 的初始值写进既有实例（app_build_info 不缺席），
//     go/process collector 序列等价直接跳过；偏离默认态各打一行 stderr
//     WARN 指明「谁占了位」；
//   - 同名异型 → 降级 stderr WARN，跳过注册与写值（既有实现占住抓取面）。
//
// 非撞名的注册错误（同名不同形状等组合态错误）显式 panic——且因
// sync.OnceFunc 的 panic 传播语义，后续**每次** Init 都会重炸同一错误，
// 修复 Registry 上的冲突前不可能静默假装成功；契约：修冲突、重启进程。
func Init(serviceName string) {
	// 首调即定（#49 契约不变）：OnceFunc 的 f 无参，生效名以第一个 CAS
	// 成功的调用为准——原子序与「首次调用」一一对应，initMetrics 读到的
	// 就是首调名；落选调用在尾部统一 WARN。
	name := serviceName
	currentService.CompareAndSwap(nil, &name)

	initOnce()

	if effective := CurrentService(); effective != serviceName {
		// stderr WARN 与 resilience.registerReuse 的降级 WARN 同款手法：
		// 本仓不引 logx——metrics 是矩阵底层仓（httpx/middleware/hertz/kitex
		// 均依赖），为零频次的启动期误用告警增加日志门面依赖不值。
		fmt.Fprintf(os.Stderr,
			"[metrics] WARN: Init(%q) 与首次调用生效的 service=%q 不一致，本次参数被忽略"+
				"——app_build_info{service} 仍是首次值，按 service 聚合的看板/告警会错标；"+
				"如需更名请重启进程\n",
			serviceName, effective)
	}
}

// initMetrics 注册内置指标并写初始值（经 initOnce 全进程恰好执行一次）。
// 只有非撞名的注册失败才返回 error；撞名已在各 register* 内降级处置。
func initMetrics() error {
	if err := registerStateless(Registry, collectors.NewGoCollector(), "go collector"); err != nil {
		return err
	}
	if err := registerStateless(Registry, collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}), "process collector"); err != nil {
		return err
	}
	buildInfo, err := registerBuildInfo(Registry)
	if err != nil {
		return err
	}
	startTime, err := registerStartTime(Registry)
	if err != nil {
		return err
	}

	// service 名此刻必然已定：Init 先 CAS currentService 再触发 initOnce。
	service := CurrentService()
	if buildInfo != nil {
		buildInfo.WithLabelValues(
			service,
			version.BuildCommit,
			version.BuildBranch,
			version.BuildTime,
			runtime.Version(),
		).Set(1)
	}
	if startTime != nil {
		startTime.Set(float64(version.StartTime.Unix()))
	}
	return nil
}

// registerStateless 注册无状态 collector（go/process collector：Init 自建
// 自弃，不保留引用），撞名容忍（#90）：既有实例供给的序列与内置版本等价，
// 幂等跳过即可——但跳过是偏离默认态的组合事件，stderr WARN 一行指明
// 「谁占了位」，替代旧版 MustRegister 的 panic（信号方向要对，#90）。
// 其余错误原样上抛，交由 initOnce 的 panic 契约处置。
func registerStateless(reg prometheus.Registerer, fresh prometheus.Collector, what string) error {
	if err := reg.Register(fresh); err != nil {
		var are prometheus.AlreadyRegisteredError
		if !errors.As(err, &are) {
			return err
		}
		fmt.Fprintf(os.Stderr,
			"[metrics] WARN: Init 跳过内置 %s——Registry 已有同名 collector（%T）占位，"+
				"序列以既有实例供给；如需内置版本请勿预注册同名指标\n",
			what, are.ExistingCollector)
	}
	return nil
}

// registerBuildInfo 注册导出变量 BuildInfo，撞名容忍（#90，registerReuse
// 同款哲学）：
//   - 既有实例就是 BuildInfo 本身（消费方预注册导出变量的合法路径）→
//     幂等返回 BuildInfo，初始值照常写（写的就是抓取面上的同一实例）；
//   - 同名同描述符的其它 *GaugeVec → 复用既有实例返回（与 registerReuse
//     的复用语义一致，app_build_info 不缺席），stderr WARN 指明导出变量
//     metrics.BuildInfo 不在抓取面上——后续直接写导出变量是指标黑洞
//     （#73 叙事）；
//   - 异型（同名不同类型）→ 降级 stderr WARN，返回 nil：初始值写值跳过，
//     app_build_info 序列以既有实现供给。
//
// 其余错误（同名不同形状等）原样上抛。
func registerBuildInfo(reg prometheus.Registerer) (*prometheus.GaugeVec, error) {
	if err := reg.Register(BuildInfo); err != nil {
		var are prometheus.AlreadyRegisteredError
		if !errors.As(err, &are) {
			return nil, err
		}
		if are.ExistingCollector == BuildInfo {
			return BuildInfo, nil
		}
		existing, ok := are.ExistingCollector.(*prometheus.GaugeVec)
		if !ok {
			fmt.Fprintf(os.Stderr,
				"[metrics] WARN: app_build_info 撞名异型（已注册 %T，请求 *prometheus.GaugeVec），"+
					"降级跳过：Init 不写初始值，app_build_info 序列以既有实现供给；"+
					"导出变量 metrics.BuildInfo 不在抓取面上，直接写它是指标黑洞\n",
				are.ExistingCollector)
			return nil, nil
		}
		fmt.Fprintf(os.Stderr,
			"[metrics] WARN: Registry 已有同名同型的 app_build_info（非导出变量本身），"+
				"Init 复用既有实例写初始值；导出变量 metrics.BuildInfo 不在抓取面上，"+
				"直接写它是指标黑洞\n")
		return existing, nil
	}
	return BuildInfo, nil
}

// registerStartTime 注册导出变量 StartTimeSeconds，撞名容忍语义与
// registerBuildInfo 完全同款（#90），契约见彼处。注意 Gauge 是接口而
// 非 *GaugeVec 那样的具体结构（NewGauge 的实现类型不导出）——同型判定
// 用 prometheus.Gauge 接口断言，初始值也经接口写入。
func registerStartTime(reg prometheus.Registerer) (prometheus.Gauge, error) {
	if err := reg.Register(StartTimeSeconds); err != nil {
		var are prometheus.AlreadyRegisteredError
		if !errors.As(err, &are) {
			return nil, err
		}
		if are.ExistingCollector == StartTimeSeconds {
			return StartTimeSeconds, nil
		}
		existing, ok := are.ExistingCollector.(prometheus.Gauge)
		if !ok {
			fmt.Fprintf(os.Stderr,
				"[metrics] WARN: app_start_time_seconds 撞名异型（已注册 %T，请求 prometheus.Gauge），"+
					"降级跳过：Init 不写初始值，app_start_time_seconds 序列以既有实现供给；"+
					"导出变量 metrics.StartTimeSeconds 不在抓取面上，直接写它是指标黑洞\n",
				are.ExistingCollector)
			return nil, nil
		}
		fmt.Fprintf(os.Stderr,
			"[metrics] WARN: Registry 已有同名同型的 app_start_time_seconds（非导出变量本身），"+
				"Init 复用既有实例写初始值；导出变量 metrics.StartTimeSeconds 不在抓取面上，"+
				"直接写它是指标黑洞\n")
		return existing, nil
	}
	return StartTimeSeconds, nil
}

// CurrentService 返回当前生效的 service 名，即首次 Init 传入的名字。
// Init 尚未调用时返回空字符串。并发安全。
func CurrentService() string {
	if p := currentService.Load(); p != nil {
		return *p
	}
	return ""
}
