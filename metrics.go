package metrics

import (
	"runtime"
	"sync"

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

// Init 注册所有通用指标并设置初始值。
// 参数 serviceName 用于 app_build_info 的 service 标签，如 "fuxi_engine"、"moyu-server"。
// 必须在 main.go 中调用，且在启动 Hertz server 之前。
// 多次调用是安全的，只有第一次调用会注册指标。
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
	})
}
