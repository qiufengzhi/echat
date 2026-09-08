// Package projection 房间授权与热状态投影层：实现 aggregate.RoleProjector 端口
//
// 分层：infrastructure——把角色变更投影到 SpiceDB、把在线心跳投影到 Redis
// 角色投影采用 write-through：signaling 在聚合收敛后实时同步，失败仅告警不阻断信令主链路
// AuthZ 未注入（global.AuthZ 为 nil）时整体判空降级返回 nil，保持纯内存运行
package projection

import (
	"context"
	"time"

	"echat-backend/global"
	"echat-backend/logging"
	"echat-backend/room/aggregate"
)

// logger projection 包的具名日志器
var logger = logging.New("projection")

// authzTimeout 单次 SpiceDB 投影或判定的超时：实时信令路径不等待授权服务
const authzTimeout = 1500 * time.Millisecond

// AuthzProjector SpiceDB 授权投影器：实现 aggregate.RoleProjector
// 无自身状态，方法内判空 global.AuthZ，未启用时静默返回 nil
type AuthzProjector struct{}

// Assign 实现 aggregate.RoleProjector：投影某用户获得某角色
func (AuthzProjector) Assign(ctx context.Context, aggID string, userID string, role string) error {
	if global.AuthZ == nil {
		return nil
	}
	if err := global.AuthZ.AssignRoomRole(ctx, role, aggID, userID); err != nil {
		logger.Warnw("授权投影失败（assign）", "aggID", aggID, "role", role, "userID", userID, "error", err)
		return err
	}
	return nil
}

// Remove 实现 aggregate.RoleProjector：投影某用户移除某角色（不存在也视为成功）
func (AuthzProjector) Remove(ctx context.Context, aggID string, userID string, role string) error {
	if global.AuthZ == nil {
		return nil
	}
	if err := global.AuthZ.RemoveRoomRole(ctx, role, aggID, userID); err != nil {
		logger.Warnw("授权投影失败（remove）", "aggID", aggID, "role", role, "userID", userID, "error", err)
		return err
	}
	return nil
}

// TransferHost 实现 aggregate.RoleProjector：原子交接房主
// 新房主原 listener 先让位保持角色卫生，再删旧 host 写新 host，保证任意时刻唯一房主
func (AuthzProjector) TransferHost(ctx context.Context, aggID string, from string, to string) error {
	if global.AuthZ == nil {
		return nil
	}
	if err := global.AuthZ.RemoveRoomRole(ctx, aggregate.RoleListener, aggID, to); err != nil {
		logger.Warnw("授权投影失败（清听众）", "aggID", aggID, "to", to, "error", err)
		return err
	}
	if err := global.AuthZ.TransferHost(ctx, aggID, from, to); err != nil {
		logger.Warnw("授权投影失败（transfer）", "aggID", aggID, "from", from, "to", to, "error", err)
		return err
	}
	return nil
}

// Can 判权查询 SpiceDB 派生权限：AuthZ 未注入时放行保持原有行为；判定出错保守拒绝
// aggID 房间聚合根 id，userID 发起者，permission 权限名（manage_ai / mod_mic / speak 等）
func Can(aggID string, userID string, permission string) bool {
	if global.AuthZ == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), authzTimeout)
	defer cancel()
	allowed, err := global.AuthZ.Can(ctx, permission, aggID, userID)
	if err != nil {
		logger.Warnw("权限判定失败，拒绝操作", "aggID", aggID, "userID", userID, "permission", permission, "error", err)
		return false
	}
	return allowed
}
