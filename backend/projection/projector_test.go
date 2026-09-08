package projection

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// testRedis 起一个内存 Redis 并返回客户端
func testRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return mr, rdb
}

// evOf 构造一条测试事件
func evOf(evType, roomCode, userID, username string) Event {
	payload, _ := json.Marshal(map[string]any{
		"room_code": roomCode,
		"user_id":   userID,
		"username":  username,
	})
	return Event{ID: "ev-1", Type: evType, Payload: payload}
}

func TestActiveRoomsProjector(t *testing.T) {
	mr, rdb := testRedis(t)
	defer mr.Close()
	p := NewActiveRoomsProjector(rdb)
	ctx := context.Background()

	if err := p.Handle(ctx, evOf("room.created", "A1", "", "")); err != nil {
		t.Fatal(err)
	}
	if in, _ := rdb.SIsMember(ctx, "active_rooms", "A1").Result(); !in {
		t.Fatal("room.created 后 A1 应在 active_rooms")
	}
	// 重复 created 幂等（SET 天然幂等）
	if err := p.Handle(ctx, evOf("room.created", "A1", "", "")); err != nil {
		t.Fatal(err)
	}
	if n, _ := rdb.SCard(ctx, "active_rooms").Result(); n != 1 {
		t.Fatalf("重复 created 不应产生重复成员，set 大小=%d", n)
	}

	if err := p.Handle(ctx, evOf("room.closed", "A1", "", "")); err != nil {
		t.Fatal(err)
	}
	if in, _ := rdb.SIsMember(ctx, "active_rooms", "A1").Result(); in {
		t.Fatal("room.closed 后 A1 应移出 active_rooms")
	}
}

func TestRoomMembersProjector(t *testing.T) {
	mr, rdb := testRedis(t)
	defer mr.Close()
	p := NewRoomMembersProjector(rdb)
	ctx := context.Background()

	if err := p.Handle(ctx, evOf("room.joined", "A1", "u1", "小李")); err != nil {
		t.Fatal(err)
	}
	if got, _ := rdb.HGet(ctx, "room:members:A1", "u1").Result(); got != "小李" {
		t.Fatalf("joined 后成员快照应为 小李，got %q", got)
	}

	if err := p.Handle(ctx, evOf("room.left", "A1", "u1", "")); err != nil {
		t.Fatal(err)
	}
	if exists, _ := rdb.HExists(ctx, "room:members:A1", "u1").Result(); exists {
		t.Fatal("left 后成员应从快照移除")
	}
}
