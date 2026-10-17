## [v0.1.7] — 2026-10-17

- fix(deps): core 地板 v0.6.0 → v0.7.0（零代码改动）

# Changelog — oekit/metrics

由 ekit v0.34.0（git.enjoye.top/enjoydream/ekit，冻结维护）拆分而来。所有 notable 变更记录如下。


## [v0.1.6] — 2026-10-17

- fix(deps): 内部依赖地板 core v0.3.2 → v0.6.0；go 最低版本降至 1.24.0——锁定 prometheus 依赖 go1.24 兼容版（client_golang v1.23.2 / procfs v0.19.2 / x/sys v0.41.0，common 随 MVS v0.67.5）

## [v0.1.0] — 2026-10-15

自 ekit v0.34.0（git.enjoye.top/enjoydream/ekit，冻结维护）拆分而来的初始公开版本，完成拆分稳定化：模块路径统一为 `github.com/oekit/metrics`，多轮拆分/合并收敛至当前模块边界，组织基建（共享 CI workflow、golangci v2、Dependabot）就位。