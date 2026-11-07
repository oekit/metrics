package metrics

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestInit_OnceOnly(t *testing.T) {
	Init("test-service")
	Init("test-service") // should not panic
}

// captureStderr 把 os.Stderr 重定向到管道并返回恢复函数与读取端。
func captureStderr(t *testing.T) (*os.File, func() []string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = w
	return r, func() []string {
		w.Close()
		os.Stderr = oldStderr
		var lines []string
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
		return lines
	}
}

// TestInit_FirstCallWins_WarnsOnMismatch 回归测试（#49）：Init 是 sync.Once
// 首调即定——此前后续调用传入不同 serviceName 被静默丢弃，app_build_info
// 的 service 标签错标且无从发现（测试先 Init("test")、生产后 Init("real")
// 即触发）。期望：生效值保持首调名 + app_build_info 不出现落选名 +
// stderr WARN 可观测；同名重复调用保持安静（多次调用安全的既有口径）。
func TestInit_FirstCallWins_WarnsOnMismatch(t *testing.T) {
	// 本测试可能先于其它测试成为进程内首次 Init：先钉住已知首调名，
	// 保证断言与测试执行顺序无关。
	if CurrentService() == "" {
		Init("init-first-service")
	}
	first := CurrentService()
	mismatch := "init-mismatched-service"
	if first == mismatch {
		t.Fatalf("构造冲突：mismatch 名 %q 与已生效 service 相同", mismatch)
	}

	_, drain := captureStderr(t)
	Init(mismatch)
	lines := drain()

	if got := CurrentService(); got != first {
		t.Fatalf("首调即定被破坏：CurrentService() = %q, want %q", got, first)
	}
	warnSeen := false
	for _, line := range lines {
		if strings.Contains(line, "WARN") && strings.Contains(line, mismatch) && strings.Contains(line, first) {
			warnSeen = true
		}
	}
	if !warnSeen {
		t.Fatalf("参数不一致时必须打 stderr WARN（含落选名与生效名），got %v", lines)
	}

	// 落选名不得进入 app_build_info——否则按 service 聚合的看板/告警错标。
	mfs, err := Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	firstSeen := false
	for _, mf := range mfs {
		if mf.GetName() != "app_build_info" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() != "service" {
					continue
				}
				if lp.GetValue() == mismatch {
					t.Fatalf("app_build_info 出现落选 service=%q（首调即定被破坏）", mismatch)
				}
				if lp.GetValue() == first {
					firstSeen = true
				}
			}
		}
	}
	if !firstSeen {
		t.Fatalf("app_build_info 缺少生效 service=%q", first)
	}

	// 同名重复调用不打 WARN。
	_, drain2 := captureStderr(t)
	Init(first)
	for _, line := range drain2() {
		if strings.Contains(line, "WARN") {
			t.Fatalf("同名重复调用不应打 WARN，got %q", line)
		}
	}
}

// TestInit_ConcurrentSafe 并发 Init 不同 service 名：-race 下无数据竞争、
// 生效值稳定为首调名（每个落选调用各自 WARN，stderr 捕获以保持测试输出干净）。
func TestInit_ConcurrentSafe(t *testing.T) {
	if CurrentService() == "" {
		Init("concurrent-base")
	}
	first := CurrentService()

	_, drain := captureStderr(t)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Init(fmt.Sprintf("concurrent-loser-%d", i))
		}()
	}
	wg.Wait()
	warnLines := drain()

	if got := CurrentService(); got != first {
		t.Fatalf("并发 Init 改写了生效 service：%q -> %q", first, got)
	}
	mismatched := 0
	for _, line := range warnLines {
		if strings.Contains(line, "WARN") && strings.Contains(line, "concurrent-loser-") {
			mismatched++
		}
	}
	if mismatched != 8 {
		t.Fatalf("8 个并发落选调用应各打一次 WARN，got %d 次：%v", mismatched, warnLines)
	}
}

func TestRegistry_NotNil(t *testing.T) {
	if Registry == nil {
		t.Error("expected non-nil Registry")
	}
}

func TestBuildInfo_NotNil(t *testing.T) {
	if BuildInfo == nil {
		t.Error("expected non-nil BuildInfo")
	}
}

func TestStartTimeSeconds_NotNil(t *testing.T) {
	if StartTimeSeconds == nil {
		t.Error("expected non-nil StartTimeSeconds")
	}
}
