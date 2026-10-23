package monitor

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// procCPU 读取 /proc/stat 中聚合的 CPU 时间。
type procCPU struct {
	total uint64
	idle  uint64
}

func readCPU() (procCPU, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return procCPU{}, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		var total, idle uint64
		for i, raw := range fields[1:] {
			v, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				continue
			}
			total += v
			// fields[1..] 中 index 3(iowait) 与 4(idle) 计入 idle。
			if i == 3 || i == 4 {
				idle += v
			}
		}
		return procCPU{total: total, idle: idle}, nil
	}
	return procCPU{}, os.ErrNotExist
}

// readMem 读取 /proc/meminfo 中的 MemTotal / MemAvailable。
func readMem() (usedBytes int64, percent float64, err error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	var totalKB, availableKB int64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			totalKB = parseKB(line)
		case strings.HasPrefix(line, "MemAvailable:"):
			availableKB = parseKB(line)
		}
		if totalKB > 0 && availableKB > 0 {
			break
		}
	}
	if totalKB <= 0 {
		return 0, 0, os.ErrNotExist
	}
	if availableKB <= 0 {
		availableKB = 0
	}
	usedKB := totalKB - availableKB
	return usedKB * 1024, float64(usedKB) / float64(totalKB) * 100, nil
}

func parseKB(line string) int64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	v, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// readUptime 返回容器运行时长（秒）。
func readUptime() int64 {
	f, err := os.Open("/proc/uptime")
	if err != nil {
		return 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return 0
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return int64(v)
}

// readNetCounters 返回非 loopback 网卡的累计收发字节。
func readNetCounters() (rx, tx int64, err error) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(line[:idx])
		if name == "lo" {
			continue
		}
		fields := strings.Fields(line[idx+1:])
		if len(fields) < 16 {
			continue
		}
		r, err1 := strconv.ParseInt(fields[0], 10, 64)
		t, err2 := strconv.ParseInt(fields[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		rx += r
		tx += t
	}
	return rx, tx, scanner.Err()
}

// diskUsage 对指定路径做容量统计。
func diskUsage(path string) (used int64, percent float64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	blockSize := int64(st.Bsize)
	total := int64(st.Blocks) * blockSize
	free := int64(st.Bavail) * blockSize
	if total <= 0 {
		return 0, 0, nil
	}
	used = total - free
	return used, float64(used) / float64(total) * 100, nil
}

// cpuTracker 通过两次采样之间的差值计算 CPU 利用率。
type cpuTracker struct {
	prev    procCPU
	started bool
}

// compute 返回自上次调用以来的 CPU 利用率百分比。
func (t *cpuTracker) compute() float64 {
	cur, err := readCPU()
	if err != nil {
		return 0
	}
	if !t.started {
		t.prev, t.started = cur, true
		return 0
	}
	totalDelta := float64(cur.total - t.prev.total)
	idleDelta := float64(cur.idle - t.prev.idle)
	t.prev = cur
	if totalDelta <= 0 {
		return 0
	}
	return clampPercent((1 - idleDelta/totalDelta) * 100)
}

// rateTracker 通过累计值差分计算速率。
type rateTracker struct {
	prevRx int64
	prevTx int64
	inited bool
}

// compute 返回自上次调用以来的速率（字节/秒）。
//
// 计数器回退保护是必须的：接口被删除重建、容器重启后 wg 的累计计数从 0
// 重新开始，此时差值为负。不做保护的话界面会显示负速率，历史曲线上出现
// 向下的尖峰，看起来像「流量倒流」。回退当拍一律返回 0 并以当前值重建基线，
// 下一拍即恢复正常速率。
func (t *rateTracker) compute(rx, tx int64, elapsed time.Duration) (int64, int64) {
	if !t.inited || elapsed <= 0 {
		t.prevRx, t.prevTx, t.inited = rx, tx, true
		return 0, 0
	}
	secs := elapsed.Seconds()
	drx, dtx := rx-t.prevRx, tx-t.prevTx
	t.prevRx, t.prevTx = rx, tx
	if drx < 0 || dtx < 0 {
		return 0, 0
	}
	return int64(float64(drx) / secs), int64(float64(dtx) / secs)
}

func clampPercent(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 100:
		return 100
	default:
		return v
	}
}

func goroutineCount() int { return runtime.NumGoroutine() }
