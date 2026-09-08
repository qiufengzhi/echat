package projection

import (
	"context"
	"encoding/json"
	"time"

	"echat-backend/authz"
	"echat-backend/room/aggregate"
	"echat-backend/room/events"
)

// reconcileTimeout 单次对账动作超时：消费路径也不无限等待授权服务
const reconcileTimeout = 1500 * time.Millisecond

// AuthzSyncProjector 授权对账投影器：消费角色相关事件 → 对账 SpiceDB 关系元组
// 快路径（write-through）仍实时写 SpiceDB；本投影器消费事件兜底，收敛快路径丢失造成的漂移
// 幂等设计：AssignRoomRole/RemoveRoomRole/TransferHost 本身 TOUCH/DELETE 幂等，重复写结果一致
type AuthzSyncProjector struct {
	// client SpiceDB 授权客户端
	client *authz.Client
}

// NewAuthzSyncProjector 构造授权对账投影器
// client 已 EnsureSchema 的授权客户端；调用方在启用 SpiceDB 时才构造并注册
func NewAuthzSyncProjector(client *authz.Client) *AuthzSyncProjector {
	return &AuthzSyncProjector{client: client}
}

// Handle 按事件类型对账目标关系元组
// 事件载荷字段约定见 room/events 目录；未识别类型视为无需对账，返回 nil
func (p *AuthzSyncProjector) Handle(ctx context.Context, ev Event) error {
	aggID := ev.AggregateID
	switch ev.Type {
	case events.EventTypeRoomJoined:
		userID, err := decodeUserID(ev.Payload)
		if err != nil {
			return err
		}
		// 首成员应为 host，由 created/host_transferred 校正；此处统一先落 listener
		return p.assign(ctx, aggID, userID, aggregate.RoleListener)

	case events.EventTypeRoomLeft:
		userID, err := decodeUserID(ev.Payload)
		if err != nil {
			return err
		}
		// 离开者清空全部互斥角色与静音叠加（DELETE 幂等，不在场也安全）
		return p.removeAll(ctx, aggID, userID)

	case events.EventTypeRoomHostTransferred:
		var body struct {
			// FromUserID 原房主
			FromUserID string `json:"from_user_id"`
			// ToUserID 新房主
			ToUserID string `json:"to_user_id"`
		}
		if err := decodePayload(ev.Payload, &body); err != nil {
			return err
		}
		return p.transferHost(ctx, aggID, body.FromUserID, body.ToUserID)

	case events.EventTypeRoomMicApproved:
		userID, err := decodeUserID(ev.Payload)
		if err != nil {
			return err
		}
		return p.assign(ctx, aggID, userID, aggregate.RoleSpeaker)

	case events.EventTypeRoomMicKicked:
		userID, err := decodeUserID(ev.Payload)
		if err != nil {
			return err
		}
		if err := p.remove(ctx, aggID, userID, aggregate.RoleSpeaker); err != nil {
			return err
		}
		return p.assign(ctx, aggID, userID, aggregate.RoleListener)

	case events.EventTypeRoomMuted:
		var body struct {
			// UserID 被静音成员
			UserID string `json:"user_id"`
			// Muted 静音态：true 静音 / false 解除
			Muted bool `json:"muted"`
		}
		if err := decodePayload(ev.Payload, &body); err != nil {
			return err
		}
		if body.Muted {
			return p.assign(ctx, aggID, body.UserID, aggregate.RoleMuted)
		}
		return p.remove(ctx, aggID, body.UserID, aggregate.RoleMuted)

	default:
		return nil
	}
}

// assign 对账一次角色授予
func (p *AuthzSyncProjector) assign(ctx context.Context, aggID, userID, role string) error {
	if userID == "" {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, reconcileTimeout)
	defer cancel()
	return p.client.AssignRoomRole(cctx, role, aggID, userID)
}

// remove 对账一次角色移除
func (p *AuthzSyncProjector) remove(ctx context.Context, aggID, userID, role string) error {
	if userID == "" {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, reconcileTimeout)
	defer cancel()
	return p.client.RemoveRoomRole(cctx, role, aggID, userID)
}

// removeAll 移除某用户在本房间的全部角色关系（互斥角色 + 静音叠加）
func (p *AuthzSyncProjector) removeAll(ctx context.Context, aggID, userID string) error {
	for _, role := range []string{
		aggregate.RoleHost,
		aggregate.RoleCohost,
		aggregate.RoleSpeaker,
		aggregate.RoleListener,
		aggregate.RoleMuted,
	} {
		if err := p.remove(ctx, aggID, userID, role); err != nil {
			return err
		}
	}
	return nil
}

// transferHost 对账房主交接（删旧 host 写新 host，防双房主）
func (p *AuthzSyncProjector) transferHost(ctx context.Context, aggID, from, to string) error {
	cctx, cancel := context.WithTimeout(ctx, reconcileTimeout)
	defer cancel()
	return p.client.TransferHost(cctx, aggID, from, to)
}

// decodeUserID 从事件载荷取 user_id 字段
func decodeUserID(raw json.RawMessage) (string, error) {
	var body struct {
		// UserID 目标成员
		UserID string `json:"user_id"`
	}
	if err := decodePayload(raw, &body); err != nil {
		return "", err
	}
	if body.UserID == "" {
		return "", nil
	}
	return body.UserID, nil
}
