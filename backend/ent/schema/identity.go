// schema/identity.go 定义登录凭据，对应 spec §2.3 的 identities 表
//
// users（你是谁）与 identities（你怎么证明）解耦：接新登录源只是在此表加一条记录
package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// IdentityProviderLocal 用户名+密码本地账号：provider_uid 为用户名
const IdentityProviderLocal = "local"

// IdentityProviderEmail 邮箱凭据：provider_uid 为邮箱本体，验证通过后绑定
const IdentityProviderEmail = "email"

// IdentityProviderPhone 手机号凭据：预留，签到后绑定
const IdentityProviderPhone = "phone"

// IdentityProviderGithub 第三方凭据：预留，provider_uid 为 GitHub openid
const IdentityProviderGithub = "github"

// Identity 一条登录凭据：某个用户在某登录来源下的唯一标识
type Identity struct {
	ent.Schema
}

// Fields 返回 Identity 的字段定义
func (Identity) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Immutable().
			Comment("主键"),
		field.UUID("user_id", uuid.UUID{}).
			Comment("归属用户 id，外键 users.id"),
		field.Enum("provider").
			Values(IdentityProviderLocal, IdentityProviderEmail, IdentityProviderPhone, IdentityProviderGithub).
			Comment("登录来源枚举：local / email / phone / github"),
		field.String("provider_uid").
			Comment("该来源下的唯一标识：邮箱本体 / 用户名 / 第三方 openid"),
		field.JSON("metadata", map[string]any{}).
			Optional().
			Comment("来源附加信息 jsonb，如第三方昵称头像，可空"),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			Comment("创建时间"),
	}
}

// Edges 返回 Identity 的入边指向：归属用户
func (Identity) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("identities").
			Unique().
			Required().
			Field("user_id").
			Comment("归属用户，外键 users.id"),
	}
}

// Indexes 声明两条唯一约束：一个登录标识只对应一个用户，一个用户在一来源只绑定一条
func (Identity) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("provider", "provider_uid").
			Unique(),
		index.Fields("provider").
			Edges("user").
			Unique(),
	}
}