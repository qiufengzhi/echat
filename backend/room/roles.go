// roles.go 房间域角色模型与 SpiceDB 授权投影
//
// 角色按用户身份（UserID）互斥：host / cohost / speaker / listener（默认）
// muted 是叠加的负向状态，不参与互斥，仅覆盖 speak 判定
// 投影采用 write-through：角色变更即同步写 SpiceDB 关系元组，失败仅告警不阻断实时链路
// AuthZ 未注入（global.AuthZ 为 nil）时整体跳过投影，保持原有纯内存行为，不影响运行
package room

import (
	"context"
	"time"

	"echat-backend/global"
)

// 角色枚举
const (
	// RoleHost 房主：房间内唯一，拥有全部管理权限
	RoleHost = "host"
	// RoleCohost 副主持：继承 host 的管理权限（manage_ai / kick / mod_mic）
	RoleCohost = "cohost"
	// RoleSpeaker 上麦者：可发言，受 muted 覆盖
	RoleSpeaker = "speaker"
	// RoleListener 听众：默认角色，可听不可言
	RoleListener = "listener"
	// RoleMuted 被静音：叠加状态，仅参与 speak 负向判定
	RoleMuted = "muted"
)

// authzTimeout 单次 SpiceDB 投影或判定的超时：实时信令路径不等待授权服务，避免拖慢主链路
const authzTimeout = 1500 * time.Millisecond

// projectAssign 把某用户的某个角色投影到 SpiceDB（write-through，重复投影幂等）
// r 目标房间，userID 成员身份，role 角色名（Role* 常量）
func projectAssign(r *Room, userID, role string) {
	if global.AuthZ == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), authzTimeout)
	defer cancel()
	if err := global.AuthZ.AssignRoomRole(ctx, role, r.AggID, userID); err != nil {
		logger.Warnw("授权投影失败（assign）", "roomID", r.ID, "role", role, "userID", userID, "error", err)
	}
}

// projectRemove 把某用户的某个角色从 SpiceDB 移除（不存在也视为成功）
func projectRemove(r *Room, userID, role string) {
	if global.AuthZ == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), authzTimeout)
	defer cancel()
	if err := global.AuthZ.RemoveRoomRole(ctx, role, r.AggID, userID); err != nil {
		logger.Warnw("授权投影失败（remove）", "roomID", r.ID, "role", role, "userID", userID, "error", err)
	}
}

// projectHostTransfer 房主交接投影：单请求内删旧 host 写新 host，保证任意时刻唯一房主
// r 目标房间，from 原房主 userID，to 新房主 userID；新房主原 listener 角色先让位保持角色卫生
func projectHostTransfer(r *Room, from, to string) {
	if global.AuthZ == nil {
		return
	}
	projectRemove(r, to, RoleListener)
	ctx, cancel := context.WithTimeout(context.Background(), authzTimeout)
	defer cancel()
	if err := global.AuthZ.TransferHost(ctx, r.AggID, from, to); err != nil {
		logger.Warnw("授权投影失败（transfer）", "roomID", r.ID, "from", from, "to", to, "error", err)
	}
}

// projectUserRemoveAll 离开者清理：删除该用户在本房间的全部关系元组（互斥角色 + muted）
// r 目标房间，userID 离开成员；内存快照在房间锁内收敛，网络删除在锁外执行
func projectUserRemoveAll(r *Room, userID string) {
	r.Lock.Lock()
	role := r.RoleOf[userID]
	muted := r.MutedOf[userID]
	delete(r.RoleOf, userID)
	delete(r.MutedOf, userID)
	r.Lock.Unlock()

	if role != "" {
		projectRemove(r, userID, role)
	}
	if muted {
		projectRemove(r, userID, RoleMuted)
	}
}

// projectUserLeave 房主离开场景的投影编排：先交接新房主，再清理离开者全部关系
// r 目标房间，wasHost 是否原房主，to 新房主（原房主离开且有值），userID 离开成员
func projectUserLeave(r *Room, wasHost bool, to, userID string) {
	if wasHost && to != "" {
		r.Lock.Lock()
		r.RoleOf[to] = RoleHost // 新房主内存角色收敛为 host
		r.Lock.Unlock()
		projectHostTransfer(r, userID, to)
	}
	projectUserRemoveAll(r, userID)
}

// canManageAI 判定用户是否可管理 AI 助手（manage_ai = host + cohost）
// AuthZ 未注入时放行保持原有行为；判定出错保守拒绝并告警
func canManageAI(roomID, userID string) bool {
	if global.AuthZ == nil {
		return true
	}
	r := getRoomByID(roomID)
	if r == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), authzTimeout)
	defer cancel()
	allowed, err := global.AuthZ.Can(ctx, "manage_ai", r.AggID, userID)
	if err != nil {
		logger.Warnw("AI 权限判定失败，拒绝操作", "roomID", roomID, "userID", userID, "error", err)
		return false
	}
	return allowed
}

// getRoomByID 按短码返回在线房间；不存在返回 nil
func getRoomByID(roomID string) *Room {
	roomLock.RLock()
	r, ok := allSignalRooms[roomID]
	roomLock.RUnlock()
	if !ok {
		return nil
	}
	return r
}