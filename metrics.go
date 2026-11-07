package metrics

import (
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

var initOnce sync.Once

// currentService 保存 Init 首次调用生效的 service 名（Init 之外只读）。
// 用 atomic 而非裸 string：CurrentService 可能与首次 Init 并发执行（#49）。
var currentService atomic.Pointer[string]

// Init 注册所有通用指标并设置初始值。
// 参数 serviceName 用于 app_build_info 的 service 标签，如 "fuxi_engine"、"moyu-server"。
// 必须在 main.go 中调用，且在启动 Hertz server 之前。
// 多次调用是安全的，只有第一次调用会注册指标；service 标签也取**第一次**
// 调用传入的名字——进程内 service 名首调即定，后续调用传入不同的
// serviceName 不会生效（打 stderr WARN 提示，不再静默丢弃，#49），
// 如需更名请重启进程。当前生效值可用 CurrentService() 查询。
func Init(serviceName string) {
	initOnce.Do(func() {
		Registry.MustRegister(collectors.NewGoCollector())
		Registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
		Registry.MustRegister(BuildInfo, StartTimeSeconds)

		BuildInfo.WithLabelValues(
			serviceName,
			version.BuildCommit,
			version.BuildBranch,
			version.BuildTime,
			runtime.Version(),
		).Set(1)
		StartTimeSeconds.Set(float64(version.StartTime.Unix()))

		currentService.Store(&serviceName)
	})
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

// CurrentService 返回当前生效的 service 名，即首次 Init 传入的名字。
// Init 尚未调用时返回空字符串。并发安全。
func CurrentService() string {
	if p := currentService.Load(); p != nil {
		return *p
	}
	return ""
}
