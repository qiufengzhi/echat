package projection

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"

	"echat-backend/room/aggregate"
	"echat-backend/room/events"
)

// reconcileTimeout 单次对账动作超时：消费路径也不无限等待授权服务
const reconcileTimeout = 1500 * time.Millisecond

// roleSync 授权同步所需的 SpiceDB 客户端视图，与 authz.Client 方法签名对齐，测试可用 fake 替换
type roleSync interface {
	// AssignRoomRole 授予某房间某角色
	AssignRoomRole(ctx context.Context, role, roomID, userID string) error
	// RemoveRoomRole 移除某房间某角色
	RemoveRoomRole(ctx context.Context, role, roomID, userID string) error
	// TransferHost 原子交接房主：删旧 host 写新 host，保证任意时刻唯一房主
	TransferHost(ctx context.Context, roomID, from, to string) error
}

// AuthzSyncProjector 授权对账投影器：消费角色相关事件 → 对账 SpiceDB 关系元组
// 快路径（write-through）仍实时写 SpiceDB；本投影器消费事件兜底，收敛快路径丢失造成的漂移
// 版本守卫：事件按 LWW by seq 水位条件应用——早于实体已应用水位的乱序重投直接跳过
// 水位与 SpiceDB 写无法原子化，采用 do-then-mark（判读 → 幂等写 → 推进水位）；单 durable consumer
// 单 goroutine 顺序消费保证同一实体不会并发判读，重投时水位未推进会原样重做
type AuthzSyncProjector struct {
	// rdb Redis 客户端，承接授权投影的 LWW 水位键
	rdb *redis.Client
	// client SpiceDB 授权客户端视图
	client roleSync
}

// NewAuthzSyncProjector 构造授权对账投影器
// rdb Redis 客户端，client 已 EnsureSchema 的授权客户端；调用方在启用 SpiceDB 时才构造并注册
func NewAuthzSyncProjector(rdb *redis.Client, client roleSync) *AuthzSyncProjector {
	return &AuthzSyncProjector{rdb: rdb, client: client}
}

// Handle 按事件类型对账目标关系元组
// 事件载荷字段约定见 room/events 目录；未识别类型视为无需对账，返回 nil
func (p *AuthzSyncProjector) Handle(ctx context.Context, ev Event) error {
	aggID := ev.AggregateID
	w := newRedisWatermark(p.rdb, authzGuardPrefix+aggID)
	switch ev.Type {
	case events.EventTypeRoomJoined:
		userID, err := decodeUserID(ev.Payload)
		if err != nil {
			return err
		}
		// 首成员应为 host，由 created/host_transferred 校正；此处统一先落 listener
		return p.applyUser(ctx, w, userID, ev.Seq, func(c context.Context) error {
			return p.assign(c, aggID, userID, aggregate.RoleListener)
		})

	case events.EventTypeRoomLeft:
		userID, err := decodeUserID(ev.Payload)
		if err != nil {
			return err
		}
		// 离开者清空全部互斥角色与静音叠加（DELETE 幂等，不在场也安全）
		return p.applyUser(ctx, w, userID, ev.Seq, func(c context.Context) error {
			return p.removeAll(c, aggID, userID)
		})

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
		return p.applyTransfer(ctx, w, aggID, body.FromUserID, body.ToUserID, ev.Seq)

	case events.EventTypeRoomMicApproved:
		userID, err := decodeUserID(ev.Payload)
		if err != nil {
			return err
		}
		return p.applyUser(ctx, w, userID, ev.Seq, func(c context.Context) error {
			return p.assign(c, aggID, userID, aggregate.RoleSpeaker)
		})

	case events.EventTypeRoomMicKicked:
		userID, err := decodeUserID(ev.Payload)
		if err != nil {
			return err
		}
		return p.applyUser(ctx, w, userID, ev.Seq, func(c context.Context) error {
			if err := p.remove(c, aggID, userID, aggregate.RoleSpeaker); err != nil {
				return err
			}
			return p.assign(c, aggID, userID, aggregate.RoleListener)
		})

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
		return p.applyUser(ctx, w, body.UserID, ev.Seq, func(c context.Context) error {
			if body.Muted {
				return p.assign(c, aggID, body.UserID, aggregate.RoleMuted)
			}
			return p.remove(c, aggID, body.UserID, aggregate.RoleMuted)
		})

	default:
		return nil
	}
}

// applyUser 按成员水位守卫执行一次授权写（joined/left/mic 等单用户事件）
// 早于已应用水位的乱序重投直接跳过；side-effect 成功后才推进水位
// ctx 链路上下文，w 房间水位，userID 目标成员，seq 事件业务序号，do 具体授权写
func (p *AuthzSyncProjector) applyUser(ctx context.Context, w *redisWatermark, userID string, seq int64, do func(context.Context) error) error {
	if userID == "" {
		return nil
	}
	seen, err := w.seen(ctx, userID, seq)
	if err != nil {
		return err
	}
	if seen {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, reconcileTimeout)
	defer cancel()
	if err := do(cctx); err != nil {
		return err
	}
	return w.mark(ctx, userID, seq)
}

// applyTransfer 房主交接版本守卫：以房间级房主水位 authzGuardHost 为唯一下限
// 早于已应用水位的陈旧交接（双向多次交接的旧条目）直接跳过，防双房主
// 交接成功后推进房主水位与双方成员水位，成员水位为尽力而为
// ctx 链路上下文，w 房间水位，aggID 房间聚合根，from/to 交接双方，seq 事件业务序号
func (p *AuthzSyncProjector) applyTransfer(ctx context.Context, w *redisWatermark, aggID, from, to string, seq int64) error {
	seen, err := w.seen(ctx, authzGuardHost, seq)
	if err != nil {
		return err
	}
	if seen {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, reconcileTimeout)
	defer cancel()
	if err := p.client.TransferHost(cctx, aggID, from, to); err != nil {
		return err
	}
	if err := w.mark(ctx, authzGuardHost, seq); err != nil {
		return err
	}
	_ = w.mark(ctx, from, seq)
	_ = w.mark(ctx, to, seq)
	return nil
}

// assign 授权一次角色授予
func (p *AuthzSyncProjector) assign(ctx context.Context, aggID, userID, role string) error {
	if userID == "" {
		return nil
	}
	return p.client.AssignRoomRole(ctx, role, aggID, userID)
}

// remove 授权一次角色移除
func (p *AuthzSyncProjector) remove(ctx context.Context, aggID, userID, role string) error {
	if userID == "" {
		return nil
	}
	return p.client.RemoveRoomRole(ctx, role, aggID, userID)
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
