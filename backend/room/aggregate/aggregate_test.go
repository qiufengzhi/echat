package aggregate

import (
	"slices"
	"testing"
	"time"
)

// fakeSession 测试用 Session 最小实现
type fakeSession struct {
	connID   string
	userID   string
	username string
	joinedAt time.Time
}

func (f fakeSession) SessionConnID() string   { return f.connID }
func (f fakeSession) SessionUserID() string   { return f.userID }
func (f fakeSession) SessionUsername() string { return f.username }
func (f fakeSession) SessionJoinedAt() time.Time {
	return f.joinedAt
}

// sess 便捷构造一条测试会话
func sess(connID, userID string) fakeSession {
	return fakeSession{connID: connID, userID: userID, username: "u-" + userID, joinedAt: time.Now()}
}

// hostOf 统计 RoleOf 中 host 角色的用户数，用于校验「无双重房主」
func hostCount(r *Room) int {
	n := 0
	for _, role := range r.RoleOf {
		if role == RoleHost {
			n++
		}
	}
	return n
}

func TestJoinFirstMemberBecomesHost(t *testing.T) {
	r := NewRoom("A1")
	o := r.Join(sess("c1", "u1"))
	if o.HostID != "u1" {
		t.Fatalf("首位成员应为房主，got %q", o.HostID)
	}
	if o.UserCount != 1 {
		t.Fatalf("人数应为 1，got %d", o.UserCount)
	}
	if o.ProjectedRole != RoleHost {
		t.Fatalf("首位成员角色应为 host，got %q", o.ProjectedRole)
	}
	if got := r.RoleOf["u1"]; got != RoleHost {
		t.Fatalf("RoleOf[u1] 应为 host，got %q", got)
	}
}

func TestJoinOrdinaryMemberBecomesListener(t *testing.T) {
	r := NewRoom("A1")
	r.Join(sess("c1", "u1"))
	o := r.Join(sess("c2", "u2"))
	if o.UserCount != 2 {
		t.Fatalf("人数应为 2，got %d", o.UserCount)
	}
	if o.ProjectedRole != RoleListener {
		t.Fatalf("普通成员角色应为 listener，got %q", o.ProjectedRole)
	}
	if o.HostID != "u1" {
		t.Fatalf("房主不应变更，got %q", o.HostID)
	}
}

func TestJoinSameUserMultiConnDoesNotOverrideRole(t *testing.T) {
	r := NewRoom("A1")
	r.Join(sess("c1", "u1")) // host
	r.Join(sess("c2", "u2")) // listener
	o := r.Join(sess("c3", "u2"))
	if o.UserCount != 3 {
		t.Fatalf("多连接也计入人数，got %d", o.UserCount)
	}
	if o.ProjectedRole != "" {
		t.Fatalf("复用既有角色时不应再分配，got %q", o.ProjectedRole)
	}
	if got := r.RoleOf["u2"]; got != RoleListener {
		t.Fatalf("u2 角色应保持 listener，got %q", got)
	}
}

func TestLeaveOrdinaryMemberOnlyRemovesMember(t *testing.T) {
	r := NewRoom("A1")
	r.Join(sess("c1", "u1")) // host
	r.Join(sess("c2", "u2")) // listener
	r.WaitingOf["u2"] = true
	r.MutedOf["u2"] = true

	o := r.Leave("c2", "u2", "")
	if o.WasHost {
		t.Fatal("普通成员离开不应标记为 wasHost")
	}
	if o.ShouldDelete {
		t.Fatal("仍有成员时不应清空房间")
	}
	if o.Remaining != 1 {
		t.Fatalf("剩余人数应为 1，got %d", o.Remaining)
	}
	if _, ok := r.Clients["c2"]; ok {
		t.Fatal("离开连接应从成员表移除")
	}
	if r.WaitingOf["u2"] {
		t.Fatal("离开应清除举手态")
	}
	if r.MutedOf["u2"] {
		t.Fatal("离开应清除静音叠加")
	}
	if r.HostID != "u1" {
		t.Fatalf("房主应保持不变，got %q", r.HostID)
	}
}

func TestLeaveHostTriggersHandover(t *testing.T) {
	r := NewRoom("A1")
	r.Join(sess("c1", "u1")) // host
	r.Join(sess("c2", "u2"))
	r.Join(sess("c3", "u3"))

	o := r.Leave("c1", "u1", "u2")
	if !o.WasHost {
		t.Fatal("房主离开应标记 wasHost")
	}
	if o.NextHostID != "u2" {
		t.Fatalf("应优先按 preferred 交接给 u2，got %q", o.NextHostID)
	}
	if r.HostID != "u2" {
		t.Fatalf("房主应更新为 u2，got %q", r.HostID)
	}
	if got := r.RoleOf["u2"]; got != RoleHost {
		t.Fatalf("新房主角色应为 host，got %q", got)
	}
	if _, ok := r.RoleOf["u1"]; ok {
		t.Fatal("离开房主的角色应被撤销")
	}
	if hostCount(r) != 1 {
		t.Fatalf("不应出现双重房主，host 数=%d", hostCount(r))
	}
}

func TestLeaveHostRandomChoiceStaysInMembers(t *testing.T) {
	r := NewRoom("A1")
	r.Join(sess("c1", "u1")) // host
	r.Join(sess("c2", "u2"))
	r.Join(sess("c3", "u3"))

	o := r.Leave("c1", "u1", "ghost-not-in-room")
	if o.NextHostID == "" || o.NextHostID == "u1" {
		t.Fatalf("next 应在剩余成员中选取，got %q", o.NextHostID)
	}
	if !slices.Contains([]string{"u2", "u3"}, o.NextHostID) {
		t.Fatalf("next 必须是剩余成员，got %q", o.NextHostID)
	}
	if r.HostID != o.NextHostID {
		t.Fatalf("HostID 应与 NextHostID 一致，got %q", r.HostID)
	}
}

func TestLeaveLastMemberMarksRoomForDelete(t *testing.T) {
	r := NewRoom("A1")
	r.Join(sess("c1", "u1"))

	o := r.Leave("c1", "u1", "")
	if !o.ShouldDelete {
		t.Fatal("房间清空应标记 ShouldDelete")
	}
	if r.HostID != "" {
		t.Fatalf("空房房主应复位，got %q", r.HostID)
	}
	if o.Remaining != 0 {
		t.Fatalf("剩余应为 0，got %d", o.Remaining)
	}
}

func TestRoleExclusivityAcrossLifecycle(t *testing.T) {
	r := NewRoom("A1")
	r.Join(sess("c1", "u1")) // host
	r.Join(sess("c2", "u2"))
	r.Join(sess("c3", "u3"))

	// 手工构造 speaker 角色后离开若干成员，保证角色表始终「一人一角色」
	r.RoleOf["u3"] = RoleSpeaker
	r.Leave("c2", "u2", "")
	r.Leave("c1", "u1", "u3")

	for user, role := range r.RoleOf {
		switch role {
		case RoleHost, RoleSpeaker, RoleListener, RoleCohost:
		default:
			t.Fatalf("用户 %s 持有非法角色 %q", user, role)
		}
	}
	if hostCount(r) > 1 {
		t.Fatalf("生命周期收敛后不应出现双重房主，host 数=%d", hostCount(r))
	}
	if got := r.RoleOf["u3"]; got != RoleHost {
		t.Fatalf("交接后 u3 应为 host，got %q", got)
	}
}

func TestUsersOrderingStable(t *testing.T) {
	r := NewRoom("A1")
	// 手动错序加入后校验 Users 按加入先后排序
	r.Join(sess("c-b", "u2"))
	time.Sleep(time.Millisecond)
	r.Join(sess("c-a", "u1"))
	r.Join(sess("c-c", "u3"))

	users := r.Users()
	if len(users) != 3 {
		t.Fatalf("应返回 3 名成员，got %d", len(users))
	}
	if users[0].UserID != "u2" || users[1].UserID != "u1" || users[2].UserID != "u3" {
		t.Fatalf("排序应按加入先后，got %+v", users)
	}
}
