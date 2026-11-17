package register

import (
	"bufio"
	"errors"
	"os"
	"strings"
	"testing"

	prom "github.com/prometheus/client_golang/prometheus"
)

// errRegisterer 是非撞名错误的注册器桩：Register 返回自带错误（不是
// AlreadyRegisteredError），钉 Reuse「非撞名错误原样上抛、不在容忍面」边界。
type errRegisterer struct{ err error }

func (e errRegisterer) Register(prom.Collector) error  { return e.err }
func (e errRegisterer) MustRegister(...prom.Collector) { panic(e.err) }
func (e errRegisterer) Unregister(prom.Collector) bool { return false }

// captureWarn 捕获一次 Reuse 调用期间的 stderr WARN（internal/warn 出口契约：
// os.Stderr 调用时取包级变量值，换入捕获文件即生效）。
func captureWarn(t *testing.T, call func()) []string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = w
	call()
	w.Close()
	os.Stderr = oldStderr
	var lines []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

// TestReuse_FirstRegister_ReturnsFresh：首次注册原样返回 fresh，collector
// 真正落进 Registry（可 Gather）。
func TestReuse_FirstRegister_ReturnsFresh(t *testing.T) {
	reg := prom.NewRegistry()
	fresh := prom.NewGaugeVec(prom.GaugeOpts{Name: "ireuse_first", Help: "h"}, nil)

	got, err := Reuse(reg, fresh)
	if err != nil {
		t.Fatal(err)
	}
	if got != fresh {
		t.Fatalf("首次注册应原样返回 fresh，got %v", got)
	}
	if _, err := reg.Gather(); err != nil {
		t.Fatalf("collector 未落进 Registry：%v", err)
	}
}

// TestReuse_NonAREError_Surfaces：非撞名注册错误不在容忍面，内核原样上抛
// （zero, err）——处置交壳（must/resilience panic、root 上抛），内核绝不静默。
func TestReuse_NonAREError_Surfaces(t *testing.T) {
	sentinel := errors.New("registry unavailable")
	got, err := Reuse(errRegisterer{err: sentinel},
		prom.NewGauge(prom.GaugeOpts{Name: "ireuse_err", Help: "h"}), WithForeignPanic("metrics"))
	if got != nil {
		t.Fatalf("非撞名错误应返回零值，got %v", got)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("非撞名错误应原样上抛，got %v", err)
	}
}

// TestReuse_ForeignPanic_ReadableError（must 面）：同名异型经 WithForeignPanic
// 得到带归属前缀的人话 error（壳 panic 前的载体），默认前缀 "metrics"、
// 空串回退默认前缀、自定义前缀透传。
func TestReuse_ForeignPanic_ReadableError(t *testing.T) {
	reg := prom.NewRegistry()
	reg.MustRegister(prom.NewCounterVec(prom.CounterOpts{Name: "ireuse_panic", Help: "h"}, []string{}))

	assertOwner := func(opts []ReuseOption, wantPrefix string) {
		t.Helper()
		fresh := prom.NewGaugeVec(prom.GaugeOpts{Name: "ireuse_panic", Help: "h"}, nil)
		got, err := Reuse(reg, fresh, opts...)
		if got != nil || err == nil {
			t.Fatalf("owner=%q：must 面异型应返回 (nil, 人话 error)，got (%v, %v)", wantPrefix, got, err)
		}
		for _, want := range []string{
			wantPrefix + ": 同名 collector 类型不符",
			"已注册 *prometheus.CounterVec",
			"请求 *prometheus.GaugeVec",
			"换 namespace",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("异型 error 文案缺 %q：%q", want, err.Error())
			}
		}
	}

	assertOwner(nil, "metrics")
	assertOwner([]ReuseOption{WithForeignPanic("")}, "metrics")
	assertOwner([]ReuseOption{WithForeignPanic("httpx")}, "httpx")
	assertOwner([]ReuseOption{WithForeignPanic("mw")}, "mw")
}

// TestReuse_ForeignZero_WarnAndZeroValue（root 容忍面）：异型降级返回零值与
// nil error（写值侧判空跳过），WARN 含指标名、reflect 推导的请求类型与
// 黑洞固定尾巴。
func TestReuse_ForeignZero_WarnAndZeroValue(t *testing.T) {
	reg := prom.NewRegistry()
	reg.MustRegister(prom.NewCounterVec(prom.CounterOpts{Name: "ireuse_zero", Help: "h"}, []string{}))

	var got *prom.GaugeVec
	lines := captureWarn(t, func() {
		g, err := Reuse[*prom.GaugeVec](reg, prom.NewGaugeVec(prom.GaugeOpts{Name: "ireuse_zero", Help: "h"}, nil),
			WithForeignZero("ireuse_zero", "metrics.SomeVar"), WithReuseWarn())
		if err != nil {
			t.Errorf("异型撞名是可观测降级而非错误：%v", err)
		}
		got = g
	})
	if got != nil {
		t.Fatalf("异型降级应返回零值（写值跳过），got %v", got)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"ireuse_zero 撞名异型",
		"已注册 *prometheus.CounterVec",
		"请求 *prometheus.GaugeVec",
		"序列以既有实现供给",
		"导出变量 metrics.SomeVar 不在抓取面上，直接写它是指标黑洞",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("异型 WARN 文案缺 %q：%v", want, lines)
		}
	}
}

// TestReuse_ForeignFresh_WarnAndUnregisteredFresh（resilience 容忍面）：异型
// 降级返回 fresh 本身，且 fresh 从未注册进 Registry（指标黑洞，#73）——WARN
// 如实说明黑洞。
func TestReuse_ForeignFresh_WarnAndUnregisteredFresh(t *testing.T) {
	reg := prom.NewRegistry()
	reg.MustRegister(prom.NewCounterVec(prom.CounterOpts{Name: "ireuse_fresh", Help: "h"}, []string{}))

	fresh := prom.NewGaugeVec(prom.GaugeOpts{Name: "ireuse_fresh", Help: "h"}, []string{})
	var got *prom.GaugeVec
	lines := captureWarn(t, func() {
		g, err := Reuse(reg, fresh, WithForeignFresh())
		if err != nil {
			t.Errorf("异型撞名是可观测降级而非错误：%v", err)
		}
		got = g
	})
	if got != fresh {
		t.Fatalf("resilience 面异型降级应返回 fresh 本身，got %v", got)
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if strings.Contains(mf.GetName(), "ireuse_fresh") && strings.Contains(mf.GetType().String(), "GAUGE") {
			t.Fatalf("降级实例不应出现在抓取面上（指标黑洞，#73）：发现 %s", mf.GetName())
		}
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"同名 collector 类型不符",
		"未注册进 Registry",
		"指标黑洞",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("异型 WARN 文案缺 %q：%v", want, lines)
		}
	}
}

// TestReuse_SameTypeReuse 可观测分野（#148 行为选项）：同名同型的**异实例**
// 复用——WithReuseWarn（root 面）打黑洞叙事 WARN；未启用（must/resilience
// 面）静默复用。两策都返回既有实例。
func TestReuse_SameTypeReuse(t *testing.T) {
	reg := prom.NewRegistry()
	foreign := prom.NewGaugeVec(prom.GaugeOpts{Name: "ireuse_same", Help: "h"}, []string{"k"})
	reg.MustRegister(foreign)

	fresh := prom.NewGaugeVec(prom.GaugeOpts{Name: "ireuse_same", Help: "h"}, []string{"k"})

	got, err := Reuse(reg, fresh, WithForeignZero("ireuse_same", "metrics.SomeVar"), WithReuseWarn())
	if err != nil {
		t.Fatal(err)
	}
	if got != foreign {
		t.Fatalf("root 面同型复用应返回既有实例，got %v", got)
	}

	got2, err := Reuse(reg, prom.NewGaugeVec(prom.GaugeOpts{Name: "ireuse_same", Help: "h"}, []string{"k"}))
	if err != nil {
		t.Fatal(err)
	}
	if got2 != foreign {
		t.Fatalf("未启用复用 WARN 时同型复用应返回既有实例，got %v", got2)
	}

	lines := captureWarn(t, func() {
		if _, err := Reuse(reg, prom.NewGaugeVec(prom.GaugeOpts{Name: "ireuse_same", Help: "h"}, []string{"k"}),
			WithForeignZero("ireuse_same", "metrics.SomeVar"), WithReuseWarn()); err != nil {
			t.Errorf("同型复用不允许报错：%v", err)
		}
	})
	if len(lines) == 0 || !strings.Contains(strings.Join(lines, "\n"),
		"Registry 已有同名同型的 ireuse_same（非导出变量本身），Init 复用既有实例写初始值") {
		t.Fatalf("启用 WithReuseWarn 的异实例复用必须打黑洞叙事 WARN：%v", lines)
	}
}

// TestReuse_SameInstance_SilentIdempotent（#90 root 面契约）：既有实例就是
// fresh 本身（消费方预注册导出变量的合法路径）→ 静默幂等返回 fresh、不打
// WARN；未启用 WithReuseWarn 的面同样返回既有实例（= fresh）。
func TestReuse_SameInstance_SilentIdempotent(t *testing.T) {
	fresh := prom.NewGaugeVec(prom.GaugeOpts{Name: "ireuse_ident", Help: "h"}, []string{"k"})

	reg := prom.NewRegistry()
	if err := reg.Register(fresh); err != nil {
		t.Fatal(err)
	}
	lines := captureWarn(t, func() {
		got, err := Reuse(reg, fresh, WithForeignZero("ireuse_ident", "metrics.SomeVar"), WithReuseWarn())
		if err != nil {
			t.Errorf("同实例幂等不允许报错：%v", err)
		}
		if got != fresh {
			t.Errorf("同实例幂等应返回 fresh 本身，got %v", got)
		}
	})
	if len(lines) != 0 {
		t.Fatalf("同实例预注册是纯幂等态，不应 WARN：%v", lines)
	}

	reg2 := prom.NewRegistry()
	if err := reg2.Register(fresh); err != nil {
		t.Fatal(err)
	}
	got2, err := Reuse(reg2, fresh, WithForeignFresh())
	if err != nil {
		t.Fatal(err)
	}
	if got2 != fresh {
		t.Fatalf("must/resilience 面同实例经类型断言自然复用，got %v", got2)
	}
}
