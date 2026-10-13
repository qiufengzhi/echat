package projection

import (
	"context"
	"encoding/json"
	"fmt"
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

// evOf 构造一条带业务序号的测试事件
// seq 单调业务序号，evType 事件类型，roomCode 房间短码，userID 成员 id，username 昵称（left 可空）
func evOf(seq int64, evType, roomCode, userID, username string) Event {
	payload := map[string]any{"room_code": roomCode}
	if userID != "" {
		payload["user_id"] = userID
	}
	if username != "" {
		payload["username"] = username
	}
	data, _ := json.Marshal(payload)
	return Event{ID: fmt.Sprintf("ev-%d", seq), Type: evType, Seq: seq, Payload: data}
}

func TestActiveRoomsProjector(t *testing.T) {
	mr, rdb := testRedis(t)
	defer mr.Close()
	p := NewActiveRoomsProjector(rdb)
	ctx := context.Background()

	if err := p.Handle(ctx, evOf(1, "room.created", "A1", "", "")); err != nil {
		t.Fatal(err)
	}
	if in, _ := rdb.SIsMember(ctx, "active_rooms", "A1").Result(); !in {
		t.Fatal("room.created 后 A1 应在 active_rooms")
	}
	// 重复 created（同号事件）幂等：水位拒绝再次应用
	if err := p.Handle(ctx, evOf(1, "room.created", "A1", "", "")); err != nil {
		t.Fatal(err)
	}
	if n, _ := rdb.SCard(ctx, "active_rooms").Result(); n != 1 {
		t.Fatalf("重复 created 不应产生重复成员，set 大小=%d", n)
	}

	if err := p.Handle(ctx, evOf(2, "room.closed", "A1", "", "")); err != nil {
		t.Fatal(err)
	}
	if in, _ := rdb.SIsMember(ctx, "active_rooms", "A1").Result(); in {
		t.Fatal("room.closed 后 A1 应移出 active_rooms")
	}
}

func TestActiveRoomsProjectorOutOfOrder(t *testing.T) {
	mr, rdb := testRedis(t)
	defer mr.Close()
	p := NewActiveRoomsProjector(rdb)
	ctx := context.Background()

	if err := p.Handle(ctx, evOf(10, "room.created", "A9", "", "")); err != nil {
		t.Fatal(err)
	}
	// 较早的 closed 晚到：水位 10，不得清除已建立的活跃状态
	if err := p.Handle(ctx, evOf(5, "room.closed", "A9", "", "")); err != nil {
		t.Fatal(err)
	}
	if in, _ := rdb.SIsMember(ctx, "active_rooms", "A9").Result(); !in {
		t.Fatal("早于水位的 closed 重投不应清除活跃状态")
	}

	if err := p.Handle(ctx, evOf(20, "room.closed", "A9", "", "")); err != nil {
		t.Fatal(err)
	}
	// 更早的 created 重投：不得把已关闭房间复活
	if err := p.Handle(ctx, evOf(10, "room.created", "A9", "", "")); err != nil {
		t.Fatal(err)
	}
	if in, _ := rdb.SIsMember(ctx, "active_rooms", "A9").Result(); in {
		t.Fatal("早于水位的 created 重投不应复活已关闭房间")
	}

	// 复开：新 created 水位更高，恢复活跃
	if err := p.Handle(ctx, evOf(21, "room.created", "A9", "", "")); err != nil {
		t.Fatal(err)
	}
	if in, _ := rdb.SIsMember(ctx, "active_rooms", "A9").Result(); !in {
		t.Fatal("复开 created 后 A9 应重新活跃")
	}
	// 复开前水位以下的 closed 重投：不得清除复开状态
	if err := p.Handle(ctx, evOf(20, "room.closed", "A9", "", "")); err != nil {
		t.Fatal(err)
	}
	if in, _ := rdb.SIsMember(ctx, "active_rooms", "A9").Result(); !in {
		t.Fatal("早于复开水位的 closed 重投不应清除活跃状态")
	}
}

func TestRoomMembersProjector(t *testing.T) {
	mr, rdb := testRedis(t)
	defer mr.Close()
	p := NewRoomMembersProjector(rdb)
	ctx := context.Background()

	if err := p.Handle(ctx, evOf(1, "room.joined", "A1", "u1", "小李")); err != nil {
		t.Fatal(err)
	}
	if got, _ := rdb.HGet(ctx, "room:members:A1", "u1").Result(); got != "小李" {
		t.Fatalf("joined 后成员快照应为 小李，got %q", got)
	}
	// 同号重复 join 幂等：水位拒绝再次应用，不产生重复
	if err := p.Handle(ctx, evOf(1, "room.joined", "A1", "u1", "小李")); err != nil {
		t.Fatal(err)
	}
	if n, _ := rdb.HLen(ctx, "room:members:A1").Result(); n != 1 {
		t.Fatalf("重复 joined 不应产生重复成员，hash 大小=%d", n)
	}

	if err := p.Handle(ctx, evOf(2, "room.left", "A1", "u1", "")); err != nil {
		t.Fatal(err)
	}
	if exists, _ := rdb.HExists(ctx, "room:members:A1", "u1").Result(); exists {
		t.Fatal("left 后成员应从快照移除")
	}
}

func TestRoomMembersProjectorOutOfOrder(t *testing.T) {
	mr, rdb := testRedis(t)
	defer mr.Close()
	p := NewRoomMembersProjector(rdb)
	ctx := context.Background()

	// 成员重新加入（水位 30）后，早先的 leave（水位 10）重投：不得误删
	if err := p.Handle(ctx, evOf(30, "room.joined", "A1", "u1", "小李")); err != nil {
		t.Fatal(err)
	}
	if err := p.Handle(ctx, evOf(10, "room.left", "A1", "u1", "")); err != nil {
		t.Fatal(err)
	}
	if got, _ := rdb.HGet(ctx, "room:members:A1", "u1").Result(); got != "小李" {
		t.Fatalf("较早 leave 晚到后成员应保留，got %q", got)
	}

	// 真正的 leave（更高水位）：正常移除
	if err := p.Handle(ctx, evOf(31, "room.left", "A1", "u1", "")); err != nil {
		t.Fatal(err)
	}
	if exists, _ := rdb.HExists(ctx, "room:members:A1", "u1").Result(); exists {
		t.Fatal("高水位 leave 后成员应移除")
	}

	// 再次加入（水位 32）后，旧 leave（水位 31）重投：不得清掉
	if err := p.Handle(ctx, evOf(32, "room.joined", "A1", "u1", "小李")); err != nil {
		t.Fatal(err)
	}
	if err := p.Handle(ctx, evOf(31, "room.left", "A1", "u1", "")); err != nil {
		t.Fatal(err)
	}
	if got, _ := rdb.HGet(ctx, "room:members:A1", "u1").Result(); got != "小李" {
		t.Fatalf("旧 leave 重投后成员应保留，got %q", got)
	}

	// 不同成员水位独立：u1 的高水位不影响 u2 的更低水位事件
	if err := p.Handle(ctx, evOf(33, "room.joined", "A1", "u2", "小张")); err != nil {
		t.Fatal(err)
	}
	if err := p.Handle(ctx, evOf(5, "room.left", "A1", "u2", "")); err != nil {
		t.Fatal(err)
	}
	if got, _ := rdb.HGet(ctx, "room:members:A1", "u2").Result(); got != "小张" {
		t.Fatalf("成员间水位应独立，u2 应保留，got %q", got)
	}
}
