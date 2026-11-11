// Package warn 提供 oekit/metrics 模块内统一的降级告警出口（#101）。
//
// 本仓的降级语义（撞名容忍、fail-loud）以「stderr WARN 可观测」为唯一信号，
// "[metrics] WARN: " 前缀 + 结尾换行是根包与 resilience 子包全部调用点的
// 既定约定；此前 10 处 fmt.Fprintf(os.Stderr, …) 内联拼写，前缀/换行约定散
// 在各调用点，措辞已在轻微漂移。本包把它收敛为单点：格式漂移只可能发生在
// 这一处。
//
// 放 internal/ 的理由：告警格式是**模块私有约定**，不对外承诺（消费方各自的
// 降级告警前缀——如 httpx 的 "httpx: "——不归本仓管）；Go 的未导出符号包级
// 可见，根包与子包无法直接共享，internal/ 是同 module 跨包共享的标准位。
//
// 约定：os.Stderr 在**调用时**取包级变量值——测试换入捕获文件即生效
// （根包 metrics_test.go / resilience/state_test.go 的 captureStderr 依赖
// 此契约，与收敛前 fmt.Fprintf(os.Stderr, …) 行为逐字节一致）。
package warn

import (
	"fmt"
	"os"
)

// Warnf 向 stderr 写一行降级告警：自动补 "[metrics] WARN: " 前缀与结尾换行。
// format 只写正文，不要自带前缀或 "\n"。
func Warnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[metrics] WARN: "+format+"\n", args...)
}
