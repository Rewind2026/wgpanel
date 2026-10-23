package httpapi

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/eventbus"
	"github.com/Rewind2026/wgpanel/internal/policy"
	"github.com/Rewind2026/wgpanel/internal/service"
	"github.com/Rewind2026/wgpanel/internal/store"
)

// M7 第 8 项：长稳（72 小时连续运行，无内存/goroutine/FD 泄漏）。
//
// 72 小时跑不满，但「泄漏」与时长无关——它是每次操作多留一个对象，
// 于是泄漏速率可以在几分钟内被测出来。这组用例把 72 小时压缩成
// 「同一批后台循环 + 同一批请求类型，跑够多个采样周期」，
// 判定口径是「后半程不高于前半程 + 余量」，而不是「跑满才算数」。
//
// 没放进来的后台循环及原因：
//   - Reconciler：单个 select 循环，只持有计数器，且它的重活全在
//     netdriver（ip/iptables 子进程）里，进程内观测不到；
//   - 定时备份循环：每小时一拍，72 小时也只有 72 次，
//     而备份本身是一次性文件操作，不存在跨拍累积。
//
// 真机 72 小时的连续采样由 /opt/.wgtest/t_stability.sh 负责，
// 它盯的是容器 RSS、FD 数与 SQLite 文件体积这三样进程内看不到的东西。

const (
	// stabilityWarmup 是稳态窗口：先跑这段时间让连接池、缓存、GC 全部进入
	// 稳态再开始比较。跳过它会把「首次建连」的一次性成本算成泄漏。
	stabilityWarmup = 4 * time.Second
	// stabilitySample 是采样周期。
	stabilitySample = 500 * time.Millisecond
	// stabilityBatch 是每个采样周期内每端点的请求轮数。
	stabilityBatch = 20
)

// TestSSESubscribeLeavesNothingBehind 是最尖锐的一条：
// SSE 连接反复建/断，每条连接都会在事件总线与采集器上各注册一个订阅。
// 漏掉任一处 defer cancel，取消函数就会随连接数线性堆积——
// 72 小时后面板会因为「开过太多次监控页」而耗尽内存。
//
// 判定用「订阅者数回到 0」而不是「goroutine 数不涨」：订阅者 map 的增长
// 是确定性的、可精确断言的；goroutine 数会被连接池、GC 与测试框架的
// 噪声干扰，判不干净。
func TestSSESubscribeLeavesNothingBehind(t *testing.T) {
	f := newLoadFixture(t, 4)
	client := f.srv.Client()

	const cycles = 60
	for i := range cycles {
		ticket, err := f.srv2Ticket(t)
		if err != nil {
			t.Fatalf("cycle %d: issue ticket: %v", i, err)
		}
		req, err := http.NewRequest(http.MethodGet,
			f.srv.URL+"/api/v1/metrics/stream?ticket="+ticket, nil)
		if err != nil {
			t.Fatalf("cycle %d: new request: %v", i, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("cycle %d: open stream: %v", i, err)
		}
		if _, err := readSSEFrame(bufio.NewReader(resp.Body)); err != nil {
			resp.Body.Close()
			t.Fatalf("cycle %d: read hello: %v", i, err)
		}
		resp.Body.Close()

		// 服务端已改成「先订阅、再发首帧」，hello 不再早于订阅，
		// 但这里仍不断言「此刻订阅者恰好为 1」——
		// 真正要钉的是「断开后必须回到 0」。少了任一处的 defer cancel，
		// 计数就再也回不到 0——这才是泄漏的定义。
		//
		// 同理，每一轮断开后都要等到归零再开下一条：客户端 Close 只是关掉管道，
		// 服务端要等到 r.Context().Done() 才回到 handler 执行 defer cancel。
		// 不等就会把「上一条还没取消」误判成泄漏——这条用例第一版就栽在这里，
		// 偶发报「订阅者应为 1，实际 2」，而代码并没有问题。
		waitNoSubscribers(t, f.bus, i)
	}
	waitNoSubscribers(t, f.bus, cycles)
}

// waitNoSubscribers 等到订阅者归零，超时即判定为泄漏。
func waitNoSubscribers(tb testing.TB, bus *eventbus.Bus, cycle int) {
	tb.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := bus.Subscribers()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			tb.Fatalf("cycle %d 断开后订阅者仍为 %d——取消函数没执行，连接数越多泄漏越大",
				cycle, n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestBackgroundLoopsDoNotAccumulateState 让有累积状态的后台循环真的跑起来：
// 策略引擎（每轮重算全站配额，内存里按天抑制重复预警）、
// 流量降采样与保留清理（调度器每小时一次，这里压到秒级）。
// 采集循环由 newLoadFixture 启动。
//
// 盯的是「循环跑完之后」而不是「跑的过程」：稳态下这些循环的内存占用
// 应当是常数——策略引擎的 warned 表按天清理、采集器的 lastPeers/online
// 每拍整体替换。任何一处被改成「增量往 map 里塞」都会在这里
// 表现为后半程高于前半程。
func TestBackgroundLoopsDoNotAccumulateState(t *testing.T) {
	f := newLoadFixture(t, 8)

	ctx, cancel := context.WithCancel(context.Background())
	engine := policy.New(f.store.PeerRepo(), f.store.SettingRepo(),
		f.store.AuditRepo(), f.bus, quietLogger())

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); engine.Start(ctx, 200*time.Millisecond) }()
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(700 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				repo := f.store.TrafficRepo()
				now := time.Now()
				_ = repo.Rollup(ctx, "10m", now.Add(-time.Hour).Unix(), 600)
				_, _ = repo.DeleteOlderThan(ctx, "raw", now.Add(-5*time.Second).Unix())
			}
		}
	}()

	early := settleAndSample(t, f, f.store)
	exerciseAndSample(t, f, f.store, early)

	cancel()
	wg.Wait()
}

// TestRepeatedPeerChurnDoesNotLeak 让 Peer 反复增删改。
//
// 这是最容易漏的一条：每次创建都要分配地址、写审计、触发一次调谐，
// 每次删除还要把地址还给 IPAM。三者任何一处只增不减，地址池就会被慢慢耗尽，
// 表现为「用着用着建不出新 Peer 了」——现场几乎无法定位。
func TestRepeatedPeerChurnDoesNotLeak(t *testing.T) {
	f := newLoadFixture(t, 0)
	admin := &authn.Principal{UserID: 1, Username: "bench", Role: domain.RoleOwner}
	ctx := context.Background()

	const rounds = 120
	live := 0
	for i := range rounds {
		p, err := f.peers.Create(ctx, service.CreateInput{
			Name: fmt.Sprintf("churn-%04d", i),
		}, admin)
		if err != nil {
			t.Fatalf("round %d create: %v", i, err)
		}
		live++

		note := "n"
		if _, err := f.peers.Update(ctx, p.ID, service.UpdateInput{Note: &note}, admin); err != nil {
			t.Fatalf("round %d update: %v", i, err)
		}
		if i%3 == 0 {
			if err := f.peers.Delete(ctx, p.ID, admin); err != nil {
				t.Fatalf("round %d delete: %v", i, err)
			}
			live--
		}
	}

	list, err := f.peers.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != live {
		t.Fatalf("存活 Peer 应为 %d，实际 %d", live, len(list))
	}

	// 关键断言：地址被完整回收。再建一批，能建满就说明池没被漏掉。
	for i := range 5 {
		if _, err := f.peers.Create(ctx, service.CreateInput{
			Name: fmt.Sprintf("probe-%d", i),
		}, admin); err != nil {
			t.Fatalf("地址池疑似泄漏：第 %d 个探针 Peer 创建失败: %v", i, err)
		}
	}
}

// settleAndSample 先空跑到稳态，再采一份基线快照。
func settleAndSample(tb testing.TB, f *loadFixture, st *store.Store) resourceSample {
	tb.Helper()
	deadline := time.Now().Add(stabilityWarmup)
	for time.Now().Before(deadline) {
		exerciseAllEndpoints(tb, f)
		time.Sleep(stabilitySample)
	}
	return sampleResources(st)
}

// exerciseAndSample 边打请求边采样，取后半程的中位数作为对比基准。
//
// 用中位数而不是最后一次采样：单次采样会被 GC 抖动、页缓存与 WAL
// checkpoint 污染，末值单点没有代表性。中位数代表「常态占用」。
func exerciseAndSample(tb testing.TB, f *loadFixture, st *store.Store, early resourceSample) {
	tb.Helper()
	const samples = 8
	goroutines := make([]int, 0, samples)
	fds := make([]int, 0, samples)
	heapKB := make([]int, 0, samples)
	rows := make([]int, 0, samples)
	lastTs := make([]int64, 0, samples)

	deadline := time.Now().Add(stabilityWarmup)
	for time.Now().Before(deadline) {
		exerciseAllEndpoints(tb, f)
		time.Sleep(stabilitySample)
		s := sampleResources(st)
		goroutines = append(goroutines, s.goroutines)
		fds = append(fds, s.fds)
		heapKB = append(heapKB, s.heapKB)
		rows = append(rows, s.sampleRows)
		lastTs = append(lastTs, s.lastSample)
	}

	late := resourceSample{
		goroutines: median(goroutines),
		fds:        median(fds),
		heapKB:     median(heapKB),
		sampleRows: rows[len(rows)-1],
		lastSample: lastTs[len(lastTs)-1],
	}

	tb.Logf("指标            预热后    后半程中位数")
	tb.Logf("goroutine      %-9d %d", early.goroutines, late.goroutines)
	tb.Logf("fd             %-9d %d", early.fds, late.fds)
	tb.Logf("heapAllocKB    %-9d %d", early.heapKB, late.heapKB)
	tb.Logf("sampleRows     %-9d %d（行数持平是保留策略在压住它，不是停止写入）",
		early.sampleRows, late.sampleRows)

	// 存活判据看最新样本的时间戳，不看行数。行数在这套参数下本来就该持平：
	// 每秒写入一批，同时每 700 ms 删掉 5 秒前的行，两者互相抵消。
	// 盯行数会把「保留策略生效」误判成「采集停了」——这条断言第一版就栽在这里。
	if late.lastSample <= early.lastSample {
		tb.Errorf("采集循环应持续写入：预热后最新样本 ts=%d，后半程 ts=%d",
			early.lastSample, late.lastSample)
	}

	assertNoGrowth(tb, "goroutine", early.goroutines, late.goroutines, 4)
	assertNoGrowth(tb, "fd", early.fds, late.fds, 2)
	assertNoGrowth(tb, "heapKB", early.heapKB, late.heapKB, 8192)
}

// exerciseAllEndpoints 打一轮仪表盘会打的全部只读端点。
func exerciseAllEndpoints(tb testing.TB, f *loadFixture) {
	tb.Helper()
	for _, path := range []string{
		"/api/v1/peers",
		"/api/v1/metrics/overview",
		"/api/v1/metrics/peers",
		"/api/v1/metrics/traffic?range=1h",
		"/api/v1/audit?pageSize=50",
		"/metrics",
		"/healthz",
		"/readyz",
	} {
		for range stabilityBatch {
			if code := f.doGet(tb, path, nil); code != http.StatusOK {
				tb.Errorf("GET %s 返回 %d", path, code)
				return
			}
		}
	}
}

type resourceSample struct {
	goroutines int
	fds        int
	heapKB     int
	sampleRows int
	lastSample int64
}

// sampleResources 采一份资源快照。
func sampleResources(st *store.Store) resourceSample {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s := resourceSample{
		goroutines: runtime.NumGoroutine(),
		fds:        openFDs(),
		heapKB:     int(ms.HeapAlloc / 1024),
	}
	var rows int
	var lastTs int64
	if err := st.DB().QueryRow(`SELECT COUNT(*), COALESCE(MAX(ts), 0) FROM traffic_sample`).
		Scan(&rows, &lastTs); err == nil {
		s.sampleRows = rows
		s.lastSample = lastTs
	}
	return s
}

// openFDs 数当前进程打开的文件描述符；读不到时返回 -1 表示不可测。
func openFDs() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

func median(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	s := append([]int(nil), xs...)
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
	return s[len(s)/2]
}

// assertNoGrowth 判定后半程没有相对前半程增长。
func assertNoGrowth(tb testing.TB, name string, before, after, slack int) {
	tb.Helper()
	if before < 0 || after < 0 {
		return // 不可测，跳过
	}
	if after <= before+slack {
		return
	}
	tb.Errorf("%s 持续增长：预热后 %d，后半程 %d（允许余量 %d）——存在泄漏",
		name, before, after, slack)
}

// quietLogger 返回丢弃输出的 logger。
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
