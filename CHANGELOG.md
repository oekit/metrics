## [v0.1.16] — 2026-11-08

- test(resilience): TestNewMetrics_NilRegFallsBackToPlatformRegistry 与轮次解耦（#55）——「reg==nil 回退平台 Registry」契约的断言此前落在进程级全局 metrics.Registry 的初始状态上：registerReuse 撞名复用既有 collector ⇒ -count=2 第二轮 ObserveRetry 在上一轮残留的同一 CounterVec 上累加，=1 断言恒红；本版起该用例先把 metrics.Registry 换成独立新 registry（用完还原）再断言，契约语义不变（回退目标仍必须是平台抓取面变量本身），resilience 包 -count=2 全绿

关联问题单：metrics#55。

## [v0.1.15] — 2026-11-08

- build: 依赖地板对齐——本版把 oekit 依赖地板抬到各依赖当前最新已发布版本（matrix-tools/scripts/align_floors.py 维护轮）

## [v0.1.14] — 2026-11-07

- fix(resilience): registerReuse 同名异型 collector 从 panic 降级为「独立实例 + stderr WARN」（#44）——**更正叙事**：v0.1.13 版本节曾记载本修复，但修复 PR #47 未随该 tag 合入（tag 快照仍是 panic 版），本版为真实落地；降级语义：新建独立实例照常返回（指标多一套同名不同型序列）、stderr WARN 可观测，Register 其它错误仍 panic
- fix(metrics): Init 首调即定——后传不同 serviceName 不再静默丢弃，打 WARN（含落选名与生效名）并新增 CurrentService() 查询当前生效值（#49）
- fix(resilience): NewMetrics(nil) 的 nil reg 回退从 prom.DefaultRegisterer 改为平台 metrics.Registry——弹性指标默认落在平台 /metrics 端点暴露的自定义 Registry 上，不再默认不可见（#50）
- build: 依赖地板对齐 core v0.13.1

关联问题单：metrics#44、metrics#49、metrics#50。

## [v0.1.13] — 2026-11-06

- **BREAKING** fix(resilience): `registerReuse` 同名异型 collector 从 panic 降级为「独立实例 + stderr WARN」（[#44](https://github.com/oekit/metrics/issues/44)）——同名同描述符但类型不同（消费方先注册过同名 GaugeVec 再换 CounterVec，或两个子系统共享 Registry 撞名）此前触发 `are.ExistingCollector.(T)` 硬断言 **panic**：Registry 是进程级共享面，撞名是运行期组合态而非启动期配置错误，一个子系统炸死整个进程不成比例。降级语义：新建独立实例照常返回（指标多一套同名不同型序列），stderr WARN 可观测；代价是降级模式**不复用也不幂等**（registry 占位仍是旧类型，每次调用新序列集）——换进程存活，命名修复后即恢复正常复用路径。边界不变：Register 的其它错误（Registry 不可用等）依旧 panic

关联问题单：metrics#44。

## [v0.1.12] — 2026-11-06

- build: 矩阵依赖地板对齐（维护轮 #233）

## [v0.1.11] — 2026-11-03

- feat(resilience): 官方状态机转换桥 CircuitStateFromBreakerValue/BreakerValue/FromName——breaker 与 resilience 数值序错位不再靠消费方手写 toCircuitState（#39）

关联问题单：metrics#39。

## [v0.1.10] — 2026-10-28

- fix(metrics): 同名 collector **类型不符**时的 panic 不可定位——既有 collector 与请求的 collector 描述符相同（fqName + help + 标签名都一致）但类型不同 ⇒ prometheus 返回 `AlreadyRegisteredError` ⇒ 无检查的类型断言 `are.ExistingCollector.(T)` 失败，panic 成一句 `interface conversion: prometheus.Collector is *prometheus.CounterVec, not *prometheus.GaugeVec`。改为有检查的断言 + 人能读懂的错误（既有类型 vs 请求类型 + 怎么改）

## [v0.1.9] — 2026-10-20

- feat(resilience): 熔断状态类型化（#15）——新增 `CircuitState` 枚举（Closed=0/HalfOpen=1/Open=2，与 gobreaker.State 数值一致）与 `ObserveTransitionTyped(service, kind, from, to CircuitState)`；`ObserveTransition(…, int, int)` 保留为历史 shim（内部显式三态映射，**产出标签逐字节不变**，kitex 等下游不受影响）。旧 int 常量与类型化枚举之间加编译期断言（数组长度必须为 0，差值非 0 即编译失败）；与 gobreaker 数值一致性的钉子放在**消费侧**（httpx），谁改数值谁编译红。

## [v0.1.8] — 2026-10-19

- build: 矩阵内依赖地板对齐（align_floors 不动点）：core v0.7.0 → v0.9.2

## [v0.1.7] — 2026-10-17

- fix(deps): core 地板 v0.6.0 → v0.7.0（零代码改动）

# Changelog — oekit/metrics

由 ekit v0.34.0（git.enjoye.top/enjoydream/ekit，冻结维护）拆分而来。所有 notable 变更记录如下。

## [v0.1.6] — 2026-10-17

- fix(deps): 内部依赖地板 core v0.3.2 → v0.6.0；go 最低版本降至 1.24.0——锁定 prometheus 依赖 go1.24 兼容版（client_golang v1.23.2 / procfs v0.19.2 / x/sys v0.41.0，common 随 MVS v0.67.5）

## [v0.1.0] — 2026-10-15

自 ekit v0.34.0（git.enjoye.top/enjoydream/ekit，冻结维护）拆分而来的初始公开版本，完成拆分稳定化：模块路径统一为 `github.com/oekit/metrics`，多轮拆分/合并收敛至当前模块边界，组织基建（共享 CI workflow、golangci v2、Dependabot）就位。