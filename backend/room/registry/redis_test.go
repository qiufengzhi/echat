package registry

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"echat-backend/room/aggregate"
)

// newTestRedis 起一个内存 Redis 并返回客户端
func newTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	return mr, redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

func TestRedisAIStore(t *testing.T) {
	mr, rdb := newTestRedis(t)
	defer mr.Close()
	s := NewRedisAIStore(rdb)

	if got := s.Get("R1"); got != "offline" {
		t.Fatalf("未设置时应为 offline，got %q", got)
	}
	s.Set("R1", "online")
	if got := s.Get("R1"); got != "online" {
		t.Fatalf("设置后应读回 online，got %q", got)
	}
	s.Remove("R1")
	if got := s.Get("R1"); got != "offline" {
		t.Fatalf("删除后应回到 offline，got %q", got)
	}
}

func TestRedisRegistryRoundTrip(t *testing.T) {
	mr, rdb := newTestRedis(t)
	defer mr.Close()
	r := NewRedisRegistry(rdb)

	room := aggregate.NewRoom("A1")
	room.AggID = "agg-1"
	room.HostID = "u1"
	room.RoleOf["u1"] = aggregate.RoleHost
	room.RoleOf["u2"] = aggregate.RoleListener
	r.Put("A1", room)

	got, ok := r.Get("A1")
	if !ok {
		t.Fatal("Put 后应能 Get")
	}
	if got.AggID != "agg-1" || got.HostID != "u1" {
		t.Fatalf("快照字段不一致，got %+v", got)
	}
	if got.RoleOf["u1"] != aggregate.RoleHost {
		t.Fatalf("角色快照应还原 host，got %q", got.RoleOf["u1"])
	}
	if !r.Exists("A1") {
		t.Fatal("Exists 应返回 true")
	}

	all := r.All()
	if len(all) != 1 || all["A1"] == nil {
		t.Fatalf("All 应枚举出 1 个房间，got %d", len(all))
	}

	r.Delete("A1")
	if r.Exists("A1") {
		t.Fatal("Delete 后应不存在")
	}
}
