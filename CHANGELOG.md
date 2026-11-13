## [v0.4.0] — 2026-11-13

- **BREAKING**：删除 resilience 历史 int 入口五件套——常量 StateClosed/StateHalfOpen/StateOpen、CircuitStateFromInt、StateName、(*Metrics).ObserveBreakerState、(*Metrics).ObserveTransition。下游迁移：State* 常量→CircuitState* 类型化枚举；CircuitStateFromInt→CircuitStateFromIntChecked（非法值返回 ErrInvalidState 而非 WARN 兜底）；StateName(i)→CircuitState.String()；ObserveBreakerState(…,int)→ObserveBreakerStateTyped(…,CircuitState)；ObserveTransition(…,int,int)→ObserveTransitionTyped(…,CircuitState,CircuitState)。裸 int 状态的跨模块数值序错位面（#39/#66/#69）就此关闭；breaker_compat 转换桥（现行契约）保留。org 内消费方 kitex/httpx/nns-endpoint 同批迁移。

## [v0.3.1] — 2026-11-13

- 4 单修复：CircuitState.String 纯函数（#104）、WithLabelLimit 四 family 记账（#105）、doc.go DefaultRegisterer（#106）、RegisterReuse nil 回退（#109）；地板对齐 core v0.15.1。

## [v0.3.0] — 2026-11-12

- feat(metrics): 导出泛型 RegisterReuse[T prom.Collector]——httpx mustRegisterReuse / middleware registerReuse 同款泛型逐字重复的全矩阵单源（#100，治本落点在本仓）：同名重复注册复用既有实例（抑制重复注册），同名异型 panic 带归属前缀的人话文案（既有类型 vs 请求类型 + 怎么改），其余注册错误 panic（MustRegister 边界）；文案前缀经 WithReuseOwner 透传（空串回退默认 "metrics"），godoc 写明 httpx/middleware wave 切换用法——两仓消费方后续 wave 切换，本版只落单源
- refactor(metrics): registerBuildInfo/registerStartTime 两份 25 行复制粘贴收敛为泛型内核 registerTolerant[T prom.Collector]（同实例幂等/同型复用+WARN/异型降级/其余上抛，语义逐字节不变），异型文案的「请求类型」改经 reflect.TypeFor[T]() 从类型参数推导，不再手拼（#99）
- refactor(metrics): 全仓 10 处内联 stderr WARN（根包 6 处 + resilience 子包 4 处）收敛为 internal/warn.Warnf 单点——自动补 "[metrics] WARN: " 前缀与换行，格式漂移只可能发生在这一处；「导出变量不在抓取面 = 指标黑洞」承诺措辞收敛为常量 warnBlackHoleTail（#101）
- build: Go 地板 1.24.0 → 1.26.0（org 全矩阵统一），ci.yml go-version 改 '1.26'（job 名不变）

关联问题单：metrics#99、metrics#100、metrics#101。

## [v0.2.4] — 2026-11-11

- fix(metrics): Init 撞名从 panic 降级为「幂等跳过/复用 + 方向正确的 stderr WARN」，注册失败后重调 Init 不再静默空转（#90）——导出变量（BuildInfo/StartTimeSeconds）被消费方在 Init 前自行注册进 Registry 或 Registry 已有同名 collector 时，旧版 `MustRegister` 直接 panic「duplicate metrics collector registration attempted」炸启动，而 sync.Once 语义下 panic 即算完成：recover 后重调 Init 整体跳过注册，app_build_info/app_start_time_seconds 永久缺失、go/process collector 卡在半注册态，唯一线索还是一条指错方向的 service WARN。修复口径：① 注册改走 Register + 显式处置——同实例已在 Registry 上幂等跳过（初始值照常写）、同名同描述符复用既有实例（resilience.registerReuse 同款哲学：BuildInfo/StartTimeSeconds 初始值写进抓取面上的实例，app_build_info 不缺席；go/process collector 序列等价直接跳过），偏离默认态各打一行指对方向的 stderr WARN，同名异型降级 WARN + 跳过写值；非撞名错误（同名不同形状等）显式 panic；② Once 换 **sync.OnceFunc**（Go 1.21+ 现代特性）：f panic 后后续每次 Init 都重炸同一 panic 值——失败可观测、契约明确（修 Registry 冲突后重启进程），「panic 后 Once-done 静默空转」整类消灭；③ service 名首调即定改为 Init 内 `currentService.CompareAndSwap`（OnceFunc 的 f 无参），#49 的 WARN 文案与语义逐字不变
- docs(metrics): doc.go 的 build.sh 注入指令改指真实注入面 `github.com/oekit/core/version` 并补全四个符号（#89）——原指令 `-X github.com/oekit/metrics/version.BuildCommit=...` 指向不存在的子包（真符号在 core/version，本包 Init 即 import 它），go tool link 对指向不存在符号的 -X **静默丢弃**（exit 0 零线索），app_build_info 的 commit/branch/build_time 恒 "unknown"，按 commit 聚合的发布核对/回滚判断/灰度对比全部失真。MOD 改为 `github.com/oekit/core`，Version/BuildCommit/BuildBranch/BuildTime 四符号逐一核对（core v0.14.0），并加防漂移钉子测试（符号改名/删除即刻编译红）

关联问题单：metrics#90、metrics#89。

## [v0.2.3] — 2026-11-10

- - build: 依赖地板对齐（第二轮发版后 floor sync，无代码变更）

## [v0.2.2] — 2026-11-10

- - build: 依赖地板对齐——core v0.13.5 → v0.13.6（floor sync，无代码变更）

## [v0.2.1] — 2026-11-10

- - docs(resilience): registerReuse 降级叙事更正——fresh 未注册、写入进不了任何抓取面（指标黑洞），WARN 文案同步（#73）

关联问题单：metrics#73。

## [v0.2.0] — 2026-11-09

- feat(resilience): ObserveBreakerStateTyped 类型化入口——非法值 fail-loud（#69）

关联问题单：metrics#69。

## [v0.1.17] — 2026-11-08

- fix(resilience): `CircuitStateFromInt` / `StateName` / `CircuitState.String()` 对非法值不再静默归 closed——fail-open「指标说谎」的最后一批历史入口关闭（#66）。修复前非法数值（不在 0/1/2 域内，典型来源是两套状态机数值序错传：gobreaker 序与 concurrency/breaker 序）走 default 分支恒产出 closed 标签——熔断器已 open，指标却说 closed，值班看到的告警面失真，与同仓 breaker_compat 转换桥「非法值返回 ErrInvalidState、绝不静默归 closed」（#39）的哲学矛盾。修复口径：① 历史 shim（签名无 error，kitex 等下游依赖）非法值降级为 fail-loud——stderr WARN 可观测（同 registerReuse 降级 WARN 先例）+ 归 CircuitStateOpen（宁可假警报，不谎报健康），ObserveTransition 历史入口经同一映射端到端产出 to="open"、gauge=2；② 新增类型化变体 `CircuitStateFromIntChecked(state int) (CircuitState, error)`——非法值返回 ErrInvalidState 包装错误，与 CircuitStateFromBreakerValue / CircuitStateFromName 同一哲学，可在边界直接拒绝；③ `String()` 对非法类型化取值（如 CircuitState(99) 强转）同口径 WARN + "open"。合法三态（0/1/2）的映射与标签产出逐字节不变，下游正确调用点零影响

关联问题单：metrics#66。

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