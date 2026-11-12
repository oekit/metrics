package metrics

import (
	"errors"
	"strings"
	"testing"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// errRegisterer 是非撞名错误的注册器桩：Register 返回自带错误（不是
// AlreadyRegisteredError），钉 RegisterReuse「其余错误 panic」边界。
type errRegisterer struct{ err error }

func (e errRegisterer) Register(prom.Collector) error  { return e.err }
func (e errRegisterer) MustRegister(...prom.Collector) { panic(e.err) }
func (e errRegisterer) Unregister(prom.Collector) bool { return false }

// TestRegisterReuse_FirstRegister_ReturnsFresh（#100）：首次注册原样返回
// fresh，collector 真正落进 Registry（可 Gather）。
func TestRegisterReuse_FirstRegister_ReturnsFresh(t *testing.T) {
	reg := prom.NewRegistry()
	fresh := prom.NewGaugeVec(prom.GaugeOpts{Name: "reuse_first", Help: "h"}, nil)

	got := RegisterReuse(reg, fresh)
	if got != fresh {
		t.Fatalf("首次注册应原样返回 fresh，got %v", got)
	}
	if _, err := reg.Gather(); err != nil {
		t.Fatalf("collector 未落进 Registry：%v", err)
	}
}

// TestRegisterReuse_DuplicateSameType_ReusesExisting（#100）：同名重复注册
// 复用既有实例——吞掉错误却返回未注册的 fresh 会静默丢第二个实例的指标。
func TestRegisterReuse_DuplicateSameType_ReusesExisting(t *testing.T) {
	reg := prom.NewRegistry()
	first := prom.NewCounterVec(prom.CounterOpts{Name: "reuse_dup", Help: "h"}, []string{"k"})
	RegisterReuse(reg, first)

	second := prom.NewCounterVec(prom.CounterOpts{Name: "reuse_dup", Help: "h"}, []string{"k"})
	got := RegisterReuse(reg, second)
	if got != first {
		t.Fatalf("同名同型重复注册必须复用既有实例（返回未注册 fresh = 指标丢失），got %v", got)
	}
}

// TestRegisterReuse_TypeMismatch_PanicsReadable（#100）：同名异型 panic 成
// 人话——含「类型不符」、既有类型、请求类型，默认归属前缀 "metrics"。
func TestRegisterReuse_TypeMismatch_PanicsReadable(t *testing.T) {
	reg := prom.NewRegistry()
	reg.MustRegister(prom.NewCounterVec(prom.CounterOpts{Name: "reuse_mismatch", Help: "h"}, []string{}))

	fresh := prom.NewGaugeVec(prom.GaugeOpts{Name: "reuse_mismatch", Help: "h"}, nil)
	recovered := recoverPanic(t, func() { RegisterReuse(reg, fresh) })
	if recovered == nil {
		t.Fatal("同名异型必须 panic（must 面：与 MustRegister 边界一致）")
	}
	msg := ""
	if err, ok := recovered.(error); ok {
		msg = err.Error()
	} else {
		t.Fatalf("panic 值应为 error（可读文案而非 interface conversion），got %T: %v", recovered, recovered)
	}
	for _, want := range []string{
		"metrics: 同名 collector 类型不符",
		"已注册 *prometheus.CounterVec",
		"请求 *prometheus.GaugeVec",
		"换 namespace",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("panic 文案缺 %q：%q", want, msg)
		}
	}
}

// TestRegisterReuse_TypeMismatch_CustomOwner（#100）：WithReuseOwner 透传
// 归属前缀——httpx/middleware wave 切换后 panic 文案与各自历史实现逐字节
// 一致；空串视为未设置，回退默认前缀。
func TestRegisterReuse_TypeMismatch_CustomOwner(t *testing.T) {
	reg := prom.NewRegistry()
	reg.MustRegister(prom.NewCounterVec(prom.CounterOpts{Name: "reuse_owner", Help: "h"}, nil))

	assertPanicPrefix := func(opts []RegisterReuseOption, wantPrefix string) {
		t.Helper()
		fresh := prom.NewHistogramVec(prom.HistogramOpts{Name: "reuse_owner", Help: "h"}, nil)
		recovered := recoverPanic(t, func() { RegisterReuse(reg, fresh, opts...) })
		if recovered == nil {
			t.Fatalf("owner=%q：同名异型必须 panic", wantPrefix)
		}
		err, ok := recovered.(error)
		if !ok {
			t.Fatalf("panic 值应为 error，got %T", recovered)
		}
		if !strings.HasPrefix(err.Error(), wantPrefix+": 同名 collector 类型不符") {
			t.Fatalf("panic 文案应以 %q 为归属前缀：%q", wantPrefix, err.Error())
		}
	}

	assertPanicPrefix([]RegisterReuseOption{WithReuseOwner("httpx")}, "httpx")
	assertPanicPrefix([]RegisterReuseOption{WithReuseOwner("mw")}, "mw")
	assertPanicPrefix([]RegisterReuseOption{WithReuseOwner("")}, "metrics")
	assertPanicPrefix(nil, "metrics")
}

// TestRegisterReuse_OtherError_Panics（#100）：非撞名注册错误原样 panic
// （与 MustRegister 边界一致，绝不静默吞掉）。
func TestRegisterReuse_OtherError_Panics(t *testing.T) {
	sentinel := errors.New("registry unavailable")
	recovered := recoverPanic(t, func() {
		RegisterReuse(errRegisterer{err: sentinel}, prom.NewGauge(prom.GaugeOpts{Name: "reuse_err", Help: "h"}))
	})
	if recovered == nil {
		t.Fatal("非撞名注册错误必须 panic（不得静默）")
	}
	if !errors.Is(recovered.(error), sentinel) {
		t.Fatalf("panic 值应携带原错误，got %v", recovered)
	}
}

// TestRegisterStartTime_ForeignOtherType_DegradesWithWarn（#99 钉子）：异型
// 文案的「请求类型」经 reflect.TypeFor[T]() 从泛型类型参数推导——StartTime
// 的 T 是接口类型 prometheus.Gauge，文案应推导出同样字符串（此前手拼，
// 收敛后防推导漂移）。
func TestRegisterStartTime_ForeignOtherType_DegradesWithWarn(t *testing.T) {
	reg := prom.NewRegistry()
	reg.MustRegister(prom.NewCounterVec(prom.CounterOpts{
		Name: "app_start_time_seconds",
		Help: "Unix timestamp of service start time",
	}, []string{}))

	_, drain := captureStderr(t)
	got, err := registerStartTime(reg)
	lines := drain()
	if err != nil {
		t.Fatalf("异型撞名是可观测降级而非错误，got: %v", err)
	}
	if got != nil {
		t.Fatalf("异型降级应返回 nil（写值跳过），got %v", got)
	}
	typeSeen, blackHoleSeen := false, false
	for _, line := range lines {
		if strings.Contains(line, "撞名异型") && strings.Contains(line, "请求 prometheus.Gauge") {
			typeSeen = true
		}
		if strings.Contains(line, "metrics.StartTimeSeconds 不在抓取面上，直接写它是指标黑洞") {
			blackHoleSeen = true
		}
	}
	if !typeSeen {
		t.Fatalf("异型 WARN 应含 reflect 推导的请求类型 prometheus.Gauge，got %v", lines)
	}
	if !blackHoleSeen {
		t.Fatalf("异型 WARN 应含收敛后的指标黑洞固定尾巴，got %v", lines)
	}
}

// TestRegisterReuse_NilRegistererFallsBackToRegistry nil Registerer 回退包级
// metrics.Registry（#109，「nil 参数 = 显式回退默认值」总则②）——此前 nil
// 直接对 nil 接口调用 Register ⇒ SIGSEGV panic，既不是回退默认值也不是
// 构造期 fail-fast。回退目标与姊妹 API resilience.NewMetrics 的 nil 回退
// 同一处（平台 /metrics 端点暴露的 registry）。
func TestRegisterReuse_NilRegistererFallsBackToRegistry(t *testing.T) {
	vec := prom.NewGaugeVec(prom.GaugeOpts{
		Name: "probe_nilreg_total",
		Help: "nil registerer fallback probe (#109)",
	}, []string{"l"})
	got := RegisterReuse[prom.Gauge](nil, vec.WithLabelValues("a"))
	if got == nil {
		t.Fatal("nil reg 应回退默认 registry 并正常返回 collector")
	}
	got.Set(42)
	// 确认真落在 metrics.Registry（Gather 可见、值为 42）
	want := `# HELP probe_nilreg_total nil registerer fallback probe (#109)
# TYPE probe_nilreg_total gauge
probe_nilreg_total{l="a"} 42
`
	if err := testutil.GatherAndCompare(Registry, strings.NewReader(want), "probe_nilreg_total"); err != nil {
		t.Fatalf("collector 未落进 metrics.Registry：%v", err)
	}

	// 同名再次以 nil reg 注册 → 走撞名复用路径返回既有实例（回退后语义不变）
	got2 := RegisterReuse[prom.Gauge](nil, vec.WithLabelValues("b"))
	if got2 != got {
		t.Fatal("同名重复注册应复用既有实例（RegisterReuse 语义在回退路径保持）")
	}
}
