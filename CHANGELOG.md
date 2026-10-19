## [v0.1.9] — 2026-10-20

- feat(resilience): 熔断状态类型化——新增 `CircuitState` 枚举（Closed=0/HalfOpen=1/Open=2，与 gobreaker.State 数值一致）与 `ObserveTransitionTyped(service, kind, from, to CircuitState)`；`ObserveTransition(…, int, int)` 保留为历史 shim（内部显式三态映射，**产出标签逐字节不变**，kitex 等下游不受影响）。旧 int 常量与类型化枚举之间加编译期断言（数组长度必须为 0，差值非 0 即编译失败）；与 gobreaker 数值一致性的钉子放在**消费侧**（httpx），谁改数值谁编译红。

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