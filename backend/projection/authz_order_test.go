package projection

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"echat-backend/room/aggregate"
)

// fakeRoleSync 记录每次授权调用的 fake，模拟房间房主元组，供乱序守卫单测使用
type fakeRoleSync struct {
	mu    sync.Mutex
	host  string   // 模拟房主元组：TransferHost / assign(host) / remove(host) 收敛
	calls []string // 按序记录每次授权动作，如 assign:listener:u1 / transfer:A:B
}

func (f *fakeRoleSync) AssignRoomRole(_ context.Context, role, _ string, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "assign:"+role+":"+userID)
	if role == aggregate.RoleHost {
		f.host = userID
	}
	return nil
}

func (f *fakeRoleSync) RemoveRoomRole(_ context.Context, role, _ string, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "remove:"+role+":"+userID)
	if role == aggregate.RoleHost && f.host == userID {
		f.host = ""
	}
	return nil
}

func (f *fakeRoleSync) TransferHost(_ context.Context, _ string, from, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "transfer:"+from+":"+to)
	if f.host == from {
		f.host = ""
	}
	f.host = to
	return nil
}

// authzEv 构造一条带业务序号与聚合根的授权相关测试事件
func authzEv(seq int64, evType string, payload map[string]any) Event {
	data, _ := json.Marshal(payload)
	return Event{ID: fmt.Sprintf("ev-%d", seq), Type: evType, Seq: seq, AggregateID: "agg-1", Payload: data}
}

// assertCalls 校验 fake 的调用序列与期望一致，不一致即 fatal
func assertCalls(t *testing.T, fake *fakeRoleSync, want []string) {
	t.Helper()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) != len(want) {
		t.Fatalf("授权调用数=%d, want %d, calls=%v", len(fake.calls), len(want), fake.calls)
	}
	for i := range want {
		if fake.calls[i] != want[i] {
			t.Fatalf("第 %d 步授权=%s, want %s, calls=%v", i, fake.calls[i], want[i], fake.calls)
		}
	}
}

// TestAuthzSyncUserOrdering 验证单用户事件的乱序守卫：陈旧旧 left 晚到不得清空已重入成员的角色
func TestAuthzSyncUserOrdering(t *testing.T) {
	mr, rdb := testRedis(t)
	defer mr.Close()
	fake := &fakeRoleSync{}
	p := NewAuthzSyncProjector(rdb, fake)
	ctx := context.Background()

	if err := p.Handle(ctx, authzEv(5, "room.joined", map[string]any{"user_id": "u1"})); err != nil {
		t.Fatal(err)
	}
	// 陈旧旧 left（seq4 < 水位 5）：乱序重投，不得清空任何角色
	if err := p.Handle(ctx, authzEv(4, "room.left", map[string]any{"user_id": "u1"})); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, fake, []string{"assign:listener:u1"})

	// 真正离开（seq6）：清空全部互斥角色与静音叠加
	if err := p.Handle(ctx, authzEv(6, "room.left", map[string]any{"user_id": "u1"})); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, fake, []string{
		"assign:listener:u1",
		"remove:host:u1", "remove:cohost:u1", "remove:speaker:u1", "remove:listener:u1", "remove:muted:u1",
	})
}

// TestAuthzSyncTransferOrdering 验证房主交接的乱序守卫：陈旧旧交接晚到不得覆盖最新房主（防双房主）
func TestAuthzSyncTransferOrdering(t *testing.T) {
	mr, rdb := testRedis(t)
	defer mr.Close()
	fake := &fakeRoleSync{}
	p := NewAuthzSyncProjector(rdb, fake)
	ctx := context.Background()

	if err := p.Handle(ctx, authzEv(5, "room.host_transferred", map[string]any{"from_user_id": "A", "to_user_id": "B"})); err != nil {
		t.Fatal(err)
	}
	if err := p.Handle(ctx, authzEv(9, "room.host_transferred", map[string]any{"from_user_id": "B", "to_user_id": "C"})); err != nil {
		t.Fatal(err)
	}
	// 陈旧旧交接（seq3 < 房主水位 9）：不得覆盖，房主保持 C，且不产生新授权写
	if err := p.Handle(ctx, authzEv(3, "room.host_transferred", map[string]any{"from_user_id": "A", "to_user_id": "B"})); err != nil {
		t.Fatal(err)
	}
	if fake.host != "C" {
		t.Fatalf("陈旧交接不得覆盖最新房主，got %q, want C", fake.host)
	}
	assertCalls(t, fake, []string{"transfer:A:B", "transfer:B:C"})
}

// TestAuthzSyncMicOrdering 验证 mic 事件的乱序守卫：陈旧旧 approved 晚到不得在 kicked 之后重新授权 speaker
func TestAuthzSyncMicOrdering(t *testing.T) {
	mr, rdb := testRedis(t)
	defer mr.Close()
	fake := &fakeRoleSync{}
	p := NewAuthzSyncProjector(rdb, fake)
	ctx := context.Background()

	// kicked 先到（seq6），approved 后到但序号更小（seq5）→ 视为乱序重投
	if err := p.Handle(ctx, authzEv(6, "room.mic_kicked", map[string]any{"user_id": "u2"})); err != nil {
		t.Fatal(err)
	}
	if err := p.Handle(ctx, authzEv(5, "room.mic_approved", map[string]any{"user_id": "u2"})); err != nil {
		t.Fatal(err)
	}
	for _, c := range fake.calls {
		if strings.Contains(c, "assign:speaker") {
			t.Fatalf("陈旧 mic_approved 不得重新授权 speaker, calls=%v", fake.calls)
		}
	}
	assertCalls(t, fake, []string{"remove:speaker:u2", "assign:listener:u2"})
}
