package metrics

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
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

// resetInitState 把 Init 的进程级状态（initOnce/currentService/Registry）
// 换成本用例的独立面并在结束时还原：进程级全局面在用例/轮次之间没有
// 隔离（#55 在 resilience 侧的同款教训），端到端 Init 用例必须自建隔离面，
// 且不得污染后续用例看到的既有状态。
func resetInitState(t *testing.T) {
	t.Helper()
	prevRegistry := Registry
	prevOnce := initOnce
	prevService := currentService.Load()
	Registry = prom.NewRegistry()
	initOnce = sync.OnceFunc(initOnceBody)
	currentService.Store(nil)
	t.Cleanup(func() {
		Registry = prevRegistry
		initOnce = prevOnce
		currentService.Store(prevService)
	})
}

// recoverPanic 执行 f 并把 panic 值返回（无 panic 返回 nil）。
func recoverPanic(t *testing.T, f func()) (recovered any) {
	t.Helper()
	defer func() {
		recovered = recover()
	}()
	f()
	return nil
}

// TestInit_PreRegisteredExports_NoPanic_BuildInfoPresent 端到端回归（#90
// 失败场景①）：消费方在 Init 前把导出变量 BuildInfo/StartTimeSeconds 自行
// 注册进 Registry——旧版 MustRegister 直接 panic「duplicate metrics
// collector registration attempted」，且 sync.Once 判定已完成，重调 Init
// 静默空转、app_build_info 永久缺失。期望：不 panic、service 名照常生效、
// app_build_info/app_start_time_seconds 出现在 Registry 上。
func TestInit_PreRegisteredExports_NoPanic_BuildInfoPresent(t *testing.T) {
	resetInitState(t)
	Registry.MustRegister(BuildInfo)
	Registry.MustRegister(StartTimeSeconds)

	if r := recoverPanic(t, func() { Init("pre-reg-svc") }); r != nil {
		t.Fatalf("预注册导出变量后 Init 不允许 panic（#90），got: %v", r)
	}
	if got := CurrentService(); got != "pre-reg-svc" {
		t.Fatalf("CurrentService() = %q, want %q", got, "pre-reg-svc")
	}
	assertBuildInfoService(t, Registry, "pre-reg-svc")
}

// TestInit_PreRegisteredForeignCollectors_DegradesObservably 端到端回归
// （#90 失败场景①变体）：Registry 预先注册了同名同型的**其它实例**与
// 自定义 go collector——期望：Init 不 panic、复用既有实例写初始值
// （app_build_info 真正出现）、跳过内置 collector 有方向正确的 WARN，
// 且不再出现指错方向的 service WARN。
func TestInit_PreRegisteredForeignCollectors_DegradesObservably(t *testing.T) {
	resetInitState(t)
	foreign := prom.NewGaugeVec(prom.GaugeOpts{
		Name: "app_build_info",
		Help: "Build information about the service",
	}, []string{"service", "commit", "branch", "build_time", "go_version"})
	Registry.MustRegister(foreign)
	Registry.MustRegister(collectors.NewGoCollector())

	_, drain := captureStderr(t)
	if r := recoverPanic(t, func() { Init("foreign-svc") }); r != nil {
		t.Fatalf("同名同型撞名不允许 panic（#90），got: %v", r)
	}
	lines := drain()

	assertBuildInfoService(t, Registry, "foreign-svc")
	reuseSeen, skipSeen, serviceWarnSeen := false, false, false
	for _, line := range lines {
		if strings.Contains(line, "复用既有实例写初始值") {
			reuseSeen = true
		}
		if strings.Contains(line, "跳过内置 go collector") {
			skipSeen = true
		}
		if strings.Contains(line, "service=") && strings.Contains(line, "不一致") {
			serviceWarnSeen = true
		}
	}
	if !reuseSeen {
		t.Fatalf("复用既有 app_build_info 实例时必须打 stderr WARN，got %v", lines)
	}
	if !skipSeen {
		t.Fatalf("跳过内置 go collector 时必须打 stderr WARN（信号指对方向），got %v", lines)
	}
	if serviceWarnSeen {
		t.Fatalf("service 名首调生效，不应出现指错方向的 service WARN（#90），got %v", lines)
	}
}

// TestInit_ShapeClash_PanicsOnEveryCall 端到端回归（#90 复合缺陷的另一半）：
// 同名不同形状（标签集不一致）不是 AlreadyRegisteredError、不在容忍面，
// Init 显式 panic；且因 sync.OnceFunc 的 panic 传播语义，**再次调用必须
// 重炸同一错误**——旧版 sync.Once 下第二次静默空转、零线索（#90 实测）。
// 契约：失败可观测、不可静默重试；修复 Registry 冲突后重启进程。
func TestInit_ShapeClash_PanicsOnEveryCall(t *testing.T) {
	resetInitState(t)
	Registry.MustRegister(prom.NewGaugeVec(prom.GaugeOpts{
		Name: "app_build_info",
		Help: "Build information about the service",
	}, []string{"service"}))

	first := recoverPanic(t, func() { Init("shape-clash") })
	if first == nil {
		t.Fatal("同名不同形状必须显式 panic（不可静默）")
	}
	second := recoverPanic(t, func() { Init("shape-clash") })
	if second == nil {
		t.Fatal("panic 后重调 Init 绝不允许静默空转（#90：Once 判定已完成）")
	}
	if fmt.Sprint(first) != fmt.Sprint(second) {
		t.Fatalf("每次调用应重炸同一错误：%v vs %v", first, second)
	}
}

// TestRegisterBuildInfo_SameInstance_SilentIdempotent（#90）：既有实例就是
// 导出变量本身（消费方预注册的合法路径）→ 幂等返回 BuildInfo、不打 WARN。
func TestRegisterBuildInfo_SameInstance_SilentIdempotent(t *testing.T) {
	reg := prom.NewRegistry()
	if err := reg.Register(BuildInfo); err != nil {
		t.Fatal(err)
	}
	_, drain := captureStderr(t)
	got, err := registerBuildInfo(reg)
	for _, line := range drain() {
		if strings.Contains(line, "WARN") {
			t.Fatalf("同实例预注册是纯幂等态，不应 WARN：%q", line)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if got != BuildInfo {
		t.Fatalf("同实例预注册应返回 BuildInfo 本身，got %v", got)
	}
}

// TestRegisterBuildInfo_ForeignSameType_ReusesExisting（#90）：同名同描述符
// 的其它 *GaugeVec 占位 → 复用既有实例返回（初始值写进抓取面上的实例，
// app_build_info 不缺席）+ WARN 指明导出变量 metrics.BuildInfo 不在抓取面。
func TestRegisterBuildInfo_ForeignSameType_ReusesExisting(t *testing.T) {
	reg := prom.NewRegistry()
	foreign := prom.NewGaugeVec(prom.GaugeOpts{
		Name: "app_build_info",
		Help: "Build information about the service",
	}, []string{"service", "commit", "branch", "build_time", "go_version"})
	reg.MustRegister(foreign)

	_, drain := captureStderr(t)
	got, err := registerBuildInfo(reg)
	lines := drain()
	if err != nil {
		t.Fatal(err)
	}
	if got != foreign {
		t.Fatalf("应复用既有同名实例，got %v", got)
	}
	warnSeen := false
	for _, line := range lines {
		if strings.Contains(line, "metrics.BuildInfo 不在抓取面") {
			warnSeen = true
		}
	}
	if !warnSeen {
		t.Fatalf("复用异实例时必须 WARN 导出变量不在抓取面（#73 叙事），got %v", lines)
	}
	// 复用的实例可写可抓取——初始值真正落在 Registry 上。
	got.WithLabelValues("svc", "c", "b", "bt", "go1.24").Set(1)
	assertBuildInfoService(t, reg, "svc")
}

// TestRegisterBuildInfo_ForeignOtherType_DegradesWithWarn（#90）：同名异型
// （占位是 CounterVec）→ WARN 降级、返回 nil（写值跳过，app_build_info
// 由既有实现供给）、不上抛错误。
func TestRegisterBuildInfo_ForeignOtherType_DegradesWithWarn(t *testing.T) {
	reg := prom.NewRegistry()
	reg.MustRegister(prom.NewCounterVec(prom.CounterOpts{
		Name: "app_build_info",
		Help: "Build information about the service",
	}, []string{"service", "commit", "branch", "build_time", "go_version"}))

	_, drain := captureStderr(t)
	got, err := registerBuildInfo(reg)
	lines := drain()
	if err != nil {
		t.Fatalf("异型撞名是可观测降级而非错误（registerReuse 同款），got: %v", err)
	}
	if got != nil {
		t.Fatalf("异型降级应返回 nil（写值跳过），got %v", got)
	}
	warnSeen := false
	for _, line := range lines {
		if strings.Contains(line, "撞名异型") {
			warnSeen = true
		}
	}
	if !warnSeen {
		t.Fatalf("异型降级必须打 stderr WARN，got %v", lines)
	}
}

// TestRegisterBuildInfo_ShapeMismatch_ErrorSurfaces（#90 容忍面边界）：
// 同名不同形状（标签集不一致）不是 AlreadyRegisteredError——错误原样
// 上抛，由 initOnce 的 panic 契约处置（绝不静默吞掉）。
func TestRegisterBuildInfo_ShapeMismatch_ErrorSurfaces(t *testing.T) {
	reg := prom.NewRegistry()
	reg.MustRegister(prom.NewGaugeVec(prom.GaugeOpts{
		Name: "app_build_info",
		Help: "Build information about the service",
	}, []string{"service"}))
	if _, err := registerBuildInfo(reg); err == nil {
		t.Fatal("同名不同形状的错误必须上抛（不在撞名容忍面）")
	}
}

// TestRegisterStartTime_ForeignSameType_ReusesExisting（#90）：StartTimeSeconds
// 与 BuildInfo 同款契约——异实例复用 + WARN 指明导出变量黑洞。
func TestRegisterStartTime_ForeignSameType_ReusesExisting(t *testing.T) {
	reg := prom.NewRegistry()
	foreign := prom.NewGauge(prom.GaugeOpts{
		Name: "app_start_time_seconds",
		Help: "Unix timestamp of service start time",
	})
	reg.MustRegister(foreign)

	_, drain := captureStderr(t)
	got, err := registerStartTime(reg)
	lines := drain()
	if err != nil {
		t.Fatal(err)
	}
	if got != foreign {
		t.Fatalf("应复用既有同名实例，got %v", got)
	}
	warnSeen := false
	for _, line := range lines {
		if strings.Contains(line, "metrics.StartTimeSeconds 不在抓取面") {
			warnSeen = true
		}
	}
	if !warnSeen {
		t.Fatalf("复用异实例时必须 WARN 导出变量不在抓取面，got %v", lines)
	}
}

// TestRegisterStateless_PreRegistered_SkipsWithWarn（#90）：Registry 预注册
// 自定义 go collector → 不 panic、跳过内置注册、WARN 指明谁占了位，
// go_* 序列仍可抓取（既有实例供给）。
func TestRegisterStateless_PreRegistered_SkipsWithWarn(t *testing.T) {
	reg := prom.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())

	_, drain := captureStderr(t)
	err := registerStateless(reg, collectors.NewGoCollector(), "go collector")
	lines := drain()
	if err != nil {
		t.Fatal(err)
	}
	warnSeen := false
	for _, line := range lines {
		if strings.Contains(line, "跳过内置 go collector") {
			warnSeen = true
		}
	}
	if !warnSeen {
		t.Fatalf("跳过内置 collector 必须打 stderr WARN，got %v", lines)
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, mf := range mfs {
		if strings.HasPrefix(mf.GetName(), "go_") {
			found = true
		}
	}
	if !found {
		t.Fatal("跳过后 go_* 序列必须仍由既有实例供给")
	}
}

// assertBuildInfoService 断言 reg 上 app_build_info 恰有一条 service 标签
// 为 want 的序列（初始值真正落在抓取面上的凭证）。
func assertBuildInfoService(t *testing.T, reg prom.Gatherer, want string) {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "app_build_info" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "service" && lp.GetValue() == want {
					return
				}
			}
		}
	}
	t.Fatalf("app_build_info 缺少 service=%q 的序列（初始值未落进抓取面）", want)
}
