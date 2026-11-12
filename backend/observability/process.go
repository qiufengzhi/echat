// process.go 进程级资源采样：CPU 使用率
//
// OTel 的 Go runtime 埋点只覆盖 GC / heap / goroutine，不含 OS 层 CPU，
// 因此这里直接读 procfs。仅 Linux 生效，其他平台静默跳过，不影响启动
package observability

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// procSamplerOnce 保证 CPU 采样协程只启动一次
var procSamplerOnce sync.Once

// clockTicksPerSecond procfs 中 utime/stime 的计数单位，Linux 默认 USER_HZ
//
// 容器内无法可靠读取 sysconf(_SC_CLK_TCK)，按 100 处理，绝对误差在百分位量级，
// 对「升级前后 CPU 相对增幅」这个用途足够
const clockTicksPerSecond = 100

// StartProcessSampler 启动进程 CPU 采样协程，重复调用安全
//
// 采集间隔 2 秒：pacer 与估计器的开销体现为稳态偏移，不需要更高频率
// 非 Linux 平台直接返回，不产生任何指标，避免造出恒为 0 的假数据
func StartProcessSampler() {
	if runtime.GOOS != "linux" {
		return
	}
	procSamplerOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()

			lastTicks, lastOK := readProcCPUTicks()
			lastAt := time.Now()
			for range ticker.C {
				ticks, ok := readProcCPUTicks()
				now := time.Now()
				if !lastOK || !ok {
					// 首次或读取失败时只重置基线，不上报
					lastTicks, lastOK, lastAt = ticks, ok, now
					continue
				}
				elapsed := now.Sub(lastAt).Seconds()
				if elapsed > 0 {
					usedSeconds := float64(ticks-lastTicks) / clockTicksPerSecond
					RecordProcessCPU(int64(usedSeconds / elapsed * 100))
				}
				lastTicks, lastAt = ticks, now
			}
		}()
	})
}

// readProcCPUTicks 读取本进程累计 CPU 时钟数（utime + stime）
//
// return: 累计时钟数与是否读取成功，读取失败时返回 0 与 false
func readProcCPUTicks() (uint64, bool) {
	raw, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, false
	}
	line := string(raw)
	// comm 字段被括号包含且自身可能含空格，必须从最后一个右括号之后开始切分
	closeIdx := strings.LastIndex(line, ")")
	if closeIdx < 0 || closeIdx+2 >= len(line) {
		return 0, false
	}
	fields := strings.Fields(line[closeIdx+2:])
	// fields[0] 是 stat 的第 3 号字段 state，因此 utime(14) 落在 fields[11]、stime(15) 落在 fields[12]
	if len(fields) < 13 {
		return 0, false
	}
	utime, errU := strconv.ParseUint(fields[11], 10, 64)
	stime, errS := strconv.ParseUint(fields[12], 10, 64)
	if errU != nil || errS != nil {
		return 0, false
	}
	return utime + stime, true
}
