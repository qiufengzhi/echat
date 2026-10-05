package dataplane

import "testing"

// findSnapshot 在运行快照里按名字查找一条记录
//
// name: 待查的结构名
// return: 找到的记录与是否存在
func findSnapshot(name string) (SnapshotEntry, bool) {
	for _, e := range Snapshot() {
		if e.Name == name {
			return e, true
		}
	}
	return SnapshotEntry{}, false
}

// TestCounterRateLimitedReport 首次必报，其后每隔 every 次上报一次
func TestCounterRateLimitedReport(t *testing.T) {
	c := NewCounter("test.counter", 5)
	defer c.Close()

	wantReport := map[int64]bool{1: true, 2: false, 3: false, 4: false, 5: true, 6: false}
	for i := int64(1); i <= 6; i++ {
		total, report := c.Inc()
		if total != i {
			t.Fatalf("第 %d 次 Inc 期望累计 %d，得到 %d", i, i, total)
		}
		if report != wantReport[i] {
			t.Fatalf("第 %d 次 Inc 期望上报 %v，得到 %v", i, wantReport[i], report)
		}
	}
}

// TestCounterDefaultEvery 告警间隔非法时取默认值，首次仍必报
func TestCounterDefaultEvery(t *testing.T) {
	c := NewCounter("test.counter.default", 0)
	defer c.Close()

	if _, report := c.Inc(); !report {
		t.Fatal("首次丢弃必须上报")
	}
	if _, report := c.Inc(); report {
		t.Fatal("第二次不应上报")
	}
}

// TestCounterDroppedAppearsInSnapshot 计数器也出现在运行快照里
func TestCounterDroppedAppearsInSnapshot(t *testing.T) {
	const name = "test.counter.snapshot"

	c := NewCounter(name, 1)
	c.Drop("reason", "unit-test")

	entry, ok := findSnapshot(name)
	if !ok {
		t.Fatal("计数器应出现在快照里")
	}
	if entry.Kind != "counter" || entry.Dropped != 1 {
		t.Fatalf("快照字段不符：%+v", entry)
	}
	if entry.Backlog != 0 || entry.Capacity != 0 {
		t.Fatalf("计数器没有积压与容量概念，期望均为 0：%+v", entry)
	}

	c.Close()
	if _, ok := findSnapshot(name); ok {
		t.Fatal("注销后的计数器不应再出现在快照里")
	}
}

// TestSnapshotSortedAndNonNil 快照按种类与名字排序，且无登记时返回空切片
func TestSnapshotSortedAndNonNil(t *testing.T) {
	Snapshot() // 触发至少一次收集，确保排序逻辑不 panic

	entries := Snapshot()
	if entries == nil {
		t.Fatal("快照应返回空切片而非 nil")
	}
	for i := 1; i < len(entries); i++ {
		prev, cur := entries[i-1], entries[i]
		if prev.Kind > cur.Kind || (prev.Kind == cur.Kind && prev.Name > cur.Name) {
			t.Fatalf("快照未排序：%+v 出现在 %+v 之前", prev, cur)
		}
	}
}
