// schema/session.go 定义会话记录，对应 spec §2.3 的 sessions 表
//
// 只存 refresh_token 的 SHA-256 哈希，不存原文；轮换制换新吊销旧，重放检测依赖哈希唯一
package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Session 一次登录会话，对应一条 refresh 令牌记录
type Session struct {
	ent.Schema
}

// Fields 返回 Session 的字段定义
func (Session) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Immutable().
			Comment("主键，同时作为会话 id 写入 access 声明的 sid"),
		field.UUID("user_id", uuid.UUID{}).
			Comment("归属用户 id，外键 users.id"),
		field.String("refresh_token_hash").
			Unique().
			Comment("refresh_token 的 SHA-256 哈希，仅存哈希不存原文，重放检测靠它唯一命中"),
		field.Int("token_version").
			Comment("签发时的用户 token_version，校验时比对，不一致即视为已失效"),
		field.Time("expires_at").
			Comment("refresh 过期时间"),
		field.Time("revoked_at").
			Optional().
			Nillable().
			Comment("吊销时间，可空；置位且保留哈希以支持重放检测"),
		field.String("ip").
			Comment("登录来源 IP（审计用，存字符串形式）"),
		field.String("user_agent").
			Optional().
			Comment("登录设备 UA（审计用）"),
		field.String("device_fingerprint").
			Optional().
			Comment("设备指纹，可空，用于多设备管理与重放检测"),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			Comment("创建时间"),
	}
}

// Edges 返回 Session 的入边指向：归属用户
func (Session) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("sessions").
			Unique().
			Required().
			Field("user_id").
			Comment("归属用户，外键 users.id"),
	}
}

// Indexes 声明按 (user_id, revoked_at) 复合索引，支撑「查活会话 / 按用户吊销全部」两类查询
func (Session) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("revoked_at").
			Edges("user"),
	}
}