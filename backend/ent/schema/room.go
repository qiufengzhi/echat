// schema/room.go 定义房间事实表，对应 roadmap P1「当前态写库」
//
// 设计要点：
//   - id 为内部聚合根 id，事件骨干按 room.{id}.{suffix} 编排，与对外展示的 room_code 短码分离
//   - host_id 指向用户表，房间关闭后保留行（status=closed），历史不删除
//   - status 由 active / closed 构成状态机，空房归档而非物理删除
package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// RoomStatusActive 可用：房间有当前在场成员，接受加入
const RoomStatusActive = "active"

// RoomStatusClosed 已关闭：房间无人归档保留，不再接受加入
const RoomStatusClosed = "closed"

// Room 一个语音房间的当前事实：房主、状态与归档时间
type Room struct {
	ent.Schema
}

// Fields 返回 Room 的字段定义
func (Room) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Immutable().
			Comment("主键，内部聚合根 id，事件骨干编排坐标"),
		field.UUID("host_id", uuid.UUID{}).
			Comment("当前房主用户 id，外键 users.id"),
		field.String("room_code").
			Unique().
			Comment("对外展示/加入用的短码，即信令层 Room.ID"),
		field.Enum("status").
			Values(RoomStatusActive, RoomStatusClosed).
			Default(RoomStatusActive).
			Comment("房间状态机：active / closed"),
		field.Time("closed_at").
			Optional().
			Nillable().
			Comment("关闭时间，可空；仅 closed 状态存在"),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			Comment("创建时间"),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			Comment("最近更新时间"),
	}
}

// Edges 返回 Room 的边：一个房间有一个房主、多个成员在场记录
func (Room) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("host", User.Type).
			Ref("hosted_rooms").
			Field("host_id").
			Unique().
			Required().
			Comment("当前房主用户，房间必须有房主"),
		edge.To("memberships", RoomMember.Type).
			Comment("房间的成员在场记录集合"),
	}
}