// schema/authtoken.go 定义一次性短命令牌，对应 spec §2.3 的 auth_tokens 表
//
// 邮箱验证与密码重置共用一张表，按 purpose 区分；只存哈希，用一次即消费
package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// AuthTokenPurposeVerifyEmail 邮箱验证 / 绑定邮箱：注册激活与补绑共用
const AuthTokenPurposeVerifyEmail = "verify_email"

// AuthTokenPurposeResetPassword 密码重置：找回密码的一次性链接
const AuthTokenPurposeResetPassword = "reset_password"

// AuthToken 一次性短命令牌：邮箱验证 / 密码重置 共用一张表
type AuthToken struct {
	ent.Schema
}

// Fields 返回 AuthToken 的字段定义
func (AuthToken) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Immutable().
			Comment("主键"),
		field.UUID("user_id", uuid.UUID{}).
			Comment("归属用户 id，外键 users.id"),
		field.Enum("purpose").
			Values(AuthTokenPurposeVerifyEmail, AuthTokenPurposeResetPassword).
			Comment("用途枚举：verify_email / reset_password"),
		field.String("token_hash").
			Comment("一次性明文 token 的 SHA-256 哈希，不存原文"),
		field.String("target").
			Optional().
			Comment("目标联系方式：verify_email 的待绑邮箱 / reset_password 的找回邮箱，可空"),
		field.Time("expires_at").
			Comment("失效时间，默认 30 分钟"),
		field.Time("consumed_at").
			Optional().
			Nillable().
			Comment("消费时间，可空；置位即失效不可复用"),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			Comment("创建时间"),
	}
}

// Edges 返回 AuthToken 的入边指向：归属用户
func (AuthToken) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("auth_tokens").
			Unique().
			Required().
			Field("user_id").
			Comment("归属用户，外键 users.id"),
	}
}

// Indexes 声明按 (user_id, purpose, consumed_at) 复合索引，
// 支撑「查某用户某用途的未消费令牌」与「同用途防滥用」
func (AuthToken) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("purpose", "consumed_at").
			Edges("user"),
	}
}