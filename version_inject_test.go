package metrics

import (
	"testing"

	"github.com/oekit/core/version"
)

// TestCoreVersionSymbols_DocumentedSurface 回归测试（#89 防漂移钉子）：
// doc.go 的 build.sh 指令注入的是 github.com/oekit/core/version 的四个导出
// 变量——此前指向不存在的 metrics/version 子包，go tool link 对指向不存在
// 符号的 -X 静默丢弃（构建日志零线索），app_build_info 恒 "unknown"。
// 本测试在编译期钉住四个符号真实存在（core/version 改名/删符号即刻编译
// 红），并钉住「未注入时默认值非空」的口径（-X 注入面以非空默认值为未
// 注入信号），防止 doc.go 与注入面再次漂移。
func TestCoreVersionSymbols_DocumentedSurface(t *testing.T) {
	if version.Version == "" || version.BuildCommit == "" || version.BuildBranch == "" || version.BuildTime == "" {
		t.Fatal("core/version 注入面符号（Version/BuildCommit/BuildBranch/BuildTime）默认值不得为空串——doc.go 的 -X 指令依赖该口径")
	}
}
