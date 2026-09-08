// Package aggregate 房间域聚合核心：在线房间状态机、成员角色与房主权威的纯领域逻辑
//
// 分层约束：本包只 import 标准库与 room/events，不触碰任何基础设施
// （store / redis / authz / pgx / global 一律不出现），落库/事件记账/授权投影全部经 ports 接口交由外部实现
// 成员连接以 Session 端口抽象注入：domain 只看得到身份与会话元数据，看不到真实传输层对象
package aggregate

import (
	"math/rand"
	"slices"
	"sync"
	"time"
)

// 成员互斥角色枚举：同一用户任意时刻只有一个角色，缺失即 listener
const (
	// RoleHost 房主：房间内唯一，拥有全部管理权限
	RoleHost = "host"
	// RoleCohost 副主持：继承 host 的管理权限（manage_ai / kick / mod_mic）
	RoleCohost = "cohost"
	// RoleSpeaker 上麦者：可发言，受 muted 覆盖
	RoleSpeaker = "speaker"
	// RoleListener 听众：默认角色，可听不可言
	RoleListener = "listener"
	// RoleMuted 被静音：叠加的负向状态，仅参与 speak 负向判定
	RoleMuted = "muted"
)

// Session 房间内一名参与者所需的领域视图，由传输层连接对象实现（见 room/gateway.Client）
// domain 据此做成员身份判定与状态变更，不依赖具体连接实现
// 方法名带 Session 前缀，规避与连接对象的导出字段同名（如 ConnID/UserID）
type Session interface {
	// SessionConnID 连接级唯一 id（服务端生成），成员表键
	SessionConnID() string
	// SessionUserID 鉴权后的权威用户 id
	SessionUserID() string
	// SessionUsername 用户进入房间时填写的展示昵称
	SessionUsername() string
	// SessionJoinedAt 加入房间时间，成员列表稳定排序依据
	SessionJoinedAt() time.Time
}

// Room 一个在线信令房间的聚合根：成员表、权威房主与互斥角色
// 仅在内存承载 active 房间；全员离开即从注册表移除，持久真相在 rooms / room_members 表
type Room struct {
	// ID 房间对外短码，由前端创建或输入（对应 rooms.room_code）
	ID string
	// AggID 房间内部聚合根 id（uuid 串），事件骨干与授权投影的编排坐标，不对外展示
	AggID string
	// HostID 当前房主的用户 ID；房主离开时会重新选择
	HostID string
	// Clients 当前在线成员，以连接 ID（ConnID）为键，值为传输层会话的领域视图
	Clients map[string]Session
	// RoleOf 成员权威角色映射（userID -> host/cohost/speaker/listener），互斥，缺失即 listener
	RoleOf map[string]string
	// MutedOf 被静音成员集合（叠加的负向状态，覆盖 speak 权限）
	MutedOf map[string]bool
	// WaitingOf 举手待上麦成员集合（userID -> 已举手），approve/reject 时收敛，leave 时清除
	WaitingOf map[string]bool
	// Lock 保护 HostID、Clients、RoleOf、MutedOf、WaitingOf 的并发读写
	Lock sync.RWMutex
}

// NewRoom 构造一个初始空的在线房间聚合，对外只暴露短码，内部聚合根 id 另行注入
// id 房间对外短码，返回初始化好各集合的房间实例
func NewRoom(id string) *Room {
	return &Room{
		ID:        id,
		Clients:   make(map[string]Session),
		RoleOf:    make(map[string]string),
		MutedOf:   make(map[string]bool),
		WaitingOf: make(map[string]bool),
	}
}

// JoinOutcome 一次成员加入聚合变更后的收敛结果，供编排层决定广播与投影
type JoinOutcome struct {
	// HostID 当前房主（首位成员加入后即自己）
	HostID string
	// UserCount 加入后的在线人数
	UserCount int
	// ProjectedRole 本次为新成员首次分配的角色，空表示复用既有角色
	ProjectedRole string
}

// Join 把一名成员纳入聚合：首成员自动为房主，普通加入者为 listener
// 同用户多连接不覆盖已分配角色（多端同开只占一个角色位）
// m 待加入会话，返回收敛后的房主、人数与新分配角色
func (r *Room) Join(m Session) JoinOutcome {
	r.Lock.Lock()
	defer r.Lock.Unlock()

	r.Clients[m.SessionConnID()] = m
	if r.HostID == "" {
		r.HostID = m.SessionUserID()
	}
	hostID := r.HostID

	role := ""
	if _, exists := r.RoleOf[m.SessionUserID()]; !exists {
		if r.HostID == m.SessionUserID() {
			r.RoleOf[m.SessionUserID()] = RoleHost
			role = RoleHost
		} else {
			r.RoleOf[m.SessionUserID()] = RoleListener
			role = RoleListener
		}
	}
	return JoinOutcome{HostID: hostID, UserCount: len(r.Clients), ProjectedRole: role}
}

// LeaveOutcome 一次成员离开聚合变更后的收敛结果，供编排层决定交接广播与落库
type LeaveOutcome struct {
	// WasHost 离开者是否原房主
	WasHost bool
	// NextHostID 离开后的房主；房间非空且有值
	NextHostID string
	// ShouldDelete 房间是否已清空（应删除内存实例与关闭事实行）
	ShouldDelete bool
	// Remaining 离开后仍在线的连接数
	Remaining int
	// RemovedRole 离开者离开前持有的互斥角色（空表示无），供授权投影撤销
	RemovedRole string
	// RemovedMuted 离开者离开前是否带静音叠加，供授权投影撤销
	RemovedMuted bool
}

// Leave 摘除一条连接并收敛房主与角色：普通成员离开只删席位，房主离开触发交接
// 交接语义沿用既有投影策略：先授予新房主 host，再无条件撤销离开者的用户级角色与静音叠加
// connID 离开的连接 id，userID 离开者身份，preferredNextHostID 房主指定的交接目标（可为空）
func (r *Room) Leave(connID string, userID string, preferredNextHostID string) LeaveOutcome {
	r.Lock.Lock()
	defer r.Lock.Unlock()

	wasHost := r.HostID == userID
	removedRole := r.RoleOf[userID]
	removedMuted := r.MutedOf[userID]
	delete(r.Clients, connID)
	delete(r.WaitingOf, userID)
	delete(r.RoleOf, userID)
	delete(r.MutedOf, userID)
	remaining := len(r.Clients)
	if remaining == 0 {
		r.HostID = ""
		return LeaveOutcome{WasHost: wasHost, ShouldDelete: true, RemovedRole: removedRole, RemovedMuted: removedMuted}
	}

	var nextHost string
	if wasHost {
		nextHost = r.chooseNextHost(preferredNextHostID)
		r.HostID = nextHost
		if nextHost != "" {
			r.RoleOf[nextHost] = RoleHost
		}
	}
	return LeaveOutcome{
		WasHost:      wasHost,
		NextHostID:   nextHost,
		Remaining:    remaining,
		RemovedRole:  removedRole,
		RemovedMuted: removedMuted,
	}
}

// HasUser 判定某用户是否仍有连接在线（多端场景留一根也算在场）
// userID 待判定用户，返回 true 表示仍在房间
func (r *Room) HasUser(userID string) bool {
	r.Lock.RLock()
	defer r.Lock.RUnlock()
	return r.hasUser(userID)
}

// hasUser 无锁版本：调用方须已持有 r.Lock
func (r *Room) hasUser(userID string) bool {
	for _, c := range r.Clients {
		if c.SessionUserID() == userID {
			return true
		}
	}
	return false
}

// chooseNextHost 在剩余成员中选下一任房主：preferred 在线则优先，否则在用户 id 中随机
// 调用方须已持有 r.Lock；空房返回空串
func (r *Room) chooseNextHost(preferredNextHostID string) string {
	if preferredNextHostID != "" && r.hasUser(preferredNextHostID) {
		return preferredNextHostID
	}

	userIDs := make([]string, 0, len(r.Clients))
	for _, c := range r.Clients {
		userIDs = append(userIDs, c.SessionUserID())
	}
	if len(userIDs) == 0 {
		return ""
	}
	slices.Sort(userIDs)
	return userIDs[rand.Intn(len(userIDs))]
}

// UserState 成员静态快照：成员列表广播与读模型共享的领域视图
type UserState struct {
	// ConnID 连接 id，多端识别用
	ConnID string
	// UserID 鉴权用户 id
	UserID string
	// Username 展示昵称
	Username string
	// Role 当前角色，缺失视作 listener
	Role string
	// JoinedAt 加入时间，排序依据
	JoinedAt time.Time
}

// Users 返回房间成员有序快照（按加入先后，其次连接 id），供广播与查询稳定排序
// 返回顺序保证同一会话内两次调用一致，让前端成员列表与席位展示尽量稳定
func (r *Room) Users() []UserState {
	r.Lock.RLock()
	states := make([]UserState, 0, len(r.Clients))
	for _, c := range r.Clients {
		role := r.RoleOf[c.SessionUserID()]
		if role == "" {
			role = RoleListener
		}
		states = append(states, UserState{
			ConnID:   c.SessionConnID(),
			UserID:   c.SessionUserID(),
			Username: c.SessionUsername(),
			Role:     role,
			JoinedAt: c.SessionJoinedAt(),
		})
	}
	r.Lock.RUnlock()

	slices.SortFunc(states, func(a, b UserState) int {
		if a.JoinedAt.Before(b.JoinedAt) {
			return -1
		}
		if a.JoinedAt.After(b.JoinedAt) {
			return 1
		}
		switch {
		case a.ConnID < b.ConnID:
			return -1
		case a.ConnID > b.ConnID:
			return 1
		default:
			return 0
		}
	})
	return states
}
