// schema/room_member.go 定义房间成员在场记录，对应 roadmap P1「当前态写库」
//
// 设计要点：
//   - 一用户对一房间仅一行，重复加入用 upsert 重置 joined_at/left_at，历史进出靠 room.* 事件日志
//   - left_at 为 null 表示在场，非空表示已离开，房间关闭时把在场行全部置已离开
//   - 成员昵称存快照，改名不影响已归档历史展示
package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// RoomMember 用户在某房间的在场事实，唯一的在场判定依据
type RoomMember struct {
	ent.Schema
}

// Fields 返回 RoomMember 的字段定义
func (RoomMember) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Immutable().
			Comment("主键，写入时由服务端生成"),
		field.UUID("room_id", uuid.UUID{}).
			Comment("所属房间 id，外键 rooms.id"),
		field.UUID("user_id", uuid.UUID{}).
			Comment("在场用户 id，外键 users.id"),
		field.String("username").
			Comment("加入时的昵称快照，用于归档展示"),
		field.Time("joined_at").
			Comment("最近一次加入时间"),
		field.Time("left_at").
			Optional().
			Nillable().
			Comment("离开时间，可空；为 null 表示当前在场"),
	}
}

// Edges 返回 RoomMember 的边：一笔在场记录属于一个房间、一个用户
func (RoomMember) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("room", Room.Type).
			Ref("memberships").
			Field("room_id").
			Unique().
			Required().
			Comment("所属房间"),
		edge.From("user", User.Type).
			Ref("room_memberships").
			Field("user_id").
			Unique().
			Required().
			Comment("在场用户"),
	}
}

// Indexes 声明 (room_id, user_id) 唯一：一用户对一房间只有一行在场记录
func (RoomMember) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("room_id", "user_id").Unique(),
	}
}