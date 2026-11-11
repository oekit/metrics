package warn

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// TestWarnf_PrefixAndNewline 钉住降级告警出口的格式契约（#101）：
// 自动补 "[metrics] WARN: " 前缀与结尾换行——全仓 10 处调用点的既定约定，
// 格式漂移只允许发生在本包这一处。
func TestWarnf_PrefixAndNewline(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = w

	Warnf("hello %s %d", "world", 42)

	w.Close()
	os.Stderr = oldStderr
	var lines []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) != 1 {
		t.Fatalf("Warnf 应输出恰好一行（自带换行，不空行），got %d 行：%q", len(lines), lines)
	}
	want := "[metrics] WARN: hello world 42"
	if lines[0] != want {
		t.Fatalf("Warnf 输出 = %q, want %q", lines[0], want)
	}
	if strings.HasSuffix(lines[0], "\n") {
		t.Fatalf("Scanner 按行切分后不应残留换行符：%q", lines[0])
	}
}
