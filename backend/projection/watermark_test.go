package projection

import (
	"context"
	"testing"
)

// TestRedisWatermark 验证 LWW 水位的判读语义：等号/更小序号已应用，更大序号未应用，实体间独立
func TestRedisWatermark(t *testing.T) {
	mr, rdb := testRedis(t)
	defer mr.Close()
	w := newRedisWatermark(rdb, "room:guard:authz:t")
	ctx := context.Background()

	if seen, _ := w.seen(ctx, "u1", 5); seen {
		t.Fatal("空水位不应判定为已应用")
	}
	if err := w.mark(ctx, "u1", 5); err != nil {
		t.Fatal(err)
	}
	if seen, _ := w.seen(ctx, "u1", 5); !seen {
		t.Fatal("等号水位应判定为已应用")
	}
	if seen, _ := w.seen(ctx, "u1", 4); !seen {
		t.Fatal("更小序号应判定为已应用")
	}
	if seen, _ := w.seen(ctx, "u1", 6); seen {
		t.Fatal("更大序号不应判定为已应用")
	}
	// 不同实体水位独立：u1 的高水位不影响 u2
	if seen, _ := w.seen(ctx, "u2", 1); seen {
		t.Fatal("不同实体水位应独立")
	}
}
