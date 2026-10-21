// Package metrics 提供压测指标采集与汇总：并发安全直方图（P50/P95/P99）与报告落盘。
// 报告结构对齐 docs/loadtest.md §2.1 指标总表的「报告呈现形式」列。
package metrics

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"sync"
	"time"
)

// Histogram 并发安全的耗时直方图（单位毫秒），采样有内存上限
type Histogram struct {
	mu   sync.Mutex
	data []float64
	maxN int
}

// NewHistogram 创建直方图；maxN 为最大采样数，超出后新样本被丢弃（防止长压测撑爆内存）
func NewHistogram(maxN int) *Histogram {
	return &Histogram{maxN: maxN}
}

// Record 记录一个毫秒值
func (h *Histogram) Record(ms float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.data) < h.maxN {
		h.data = append(h.data, ms)
	}
}

// Summary 直方图分位数汇总（JSON 序列化友好）
type Summary struct {
	Count int     `json:"count"`
	P50   float64 `json:"p50"`
	P95   float64 `json:"p95"`
	P99   float64 `json:"p99"`
	Max   float64 `json:"max"`
}

// Summarize 计算分位数；无样本时返回零值
func (h *Histogram) Summarize() Summary {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.data) == 0 {
		return Summary{}
	}
	sort.Float64s(h.data)
	pick := func(p float64) float64 {
		idx := int(math.Ceil(p*float64(len(h.data)))) - 1
		if idx < 0 {
			idx = 0
		}
		return h.data[idx]
	}
	return Summary{
		Count: len(h.data),
		P50:   pick(0.5),
		P95:   pick(0.95),
		P99:   pick(0.99),
		Max:   h.data[len(h.data)-1],
	}
}

// Report 一次压测 run 的完整报告，落盘为 loadtest/results/<runId>.json
type Report struct {
	RunID     string         `json:"runId"`
	Scenario  string         `json:"scenario"`
	StartedAt time.Time      `json:"startedAt"`
	DurationS float64        `json:"durationSec"`
	Params    map[string]any `json:"params"`
	Metrics   map[string]any `json:"metrics"`
}

// Save 将报告以缩进 JSON 写入指定路径
func (r *Report) Save(path string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
