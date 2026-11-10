// Package metrics 提供通用的 Prometheus 指标注册和初始化（framework-agnostic）。
//
// 所有 enjoydream 后端服务共享此包，保持指标命名和标签的一致性。
// 本包只暴露 Registry 与指标类型，不绑定任何 HTTP 框架；
// HTTP 适配器位于 hertz.MetricsHandler / hertz.MetricsHandlerFor / hertz.InitAndRegister。
//
// # 提供的指标
//
//   - app_build_info{service, commit, branch, build_time, go_version} — 构建信息
//   - app_start_time_seconds — 进程启动时间戳
//
// # 快速接入
//
// 在 main.go 中：
//
//	metrics.Init("my-service")
//	// Hertz 适配器：
//	r.GET("/metrics", hertz.MetricsHandler())
//	// 或使用自定义 Registry（推荐与 middleware.PrometheusMetrics 共享）：
//	r.GET("/metrics", hertz.MetricsHandlerFor(metrics.Registry))
//
// # build.sh 改动
//
// 构建元数据符号在 github.com/oekit/core/version（本包 Init 读同一组变量
// 生成 app_build_info），build.sh 注入时 MOD 必须指向 core——此前指向
// ${MOD}/version（metrics 下不存在该子包）时，go tool link 对指向不存在
// 符号的 -X 静默丢弃（exit 0 零线索），app_build_info 恒为 "unknown"（#89）：
//
//	MOD="github.com/oekit/core"
//	LDFLAGS="-X ${MOD}/version.Version=${VERSION} \
//	         -X ${MOD}/version.BuildCommit=${GIT_COMMIT} \
//	         -X ${MOD}/version.BuildBranch=${GIT_BRANCH} \
//	         -X ${MOD}/version.BuildTime=${BUILD_TIME}"
//
// # 自定义 Registry
//
// 使用独立的 Registry 避免与全局 prometheus.DefaultRegistry 冲突：
//
//	registry := metrics.Registry
//	// 传给 middleware.NewPrometheusMetrics 或 hertz.MetricsHandlerFor
package metrics
