// schema/user.go 定义用户身份本体，对应 spec §2.3 的 users 表
//
// 设计要点：
//   - email 可空（本地账号未绑定时为 NULL），登录凭据在 identities，邮箱仅作展示与找回
//   - password_hash 是账户级主凭据，local 与 email 登录共用同一份密码
//   - status 由 pending / active / suspended / deleted 构成状态机
//   - email 的唯一索引为部分索引（WHERE email IS NOT NULL），多个无邮箱本地账号可共存
//   - id 由服务端显式生成（uuid.New()），不依赖数据库默认值，便于跨库可移植
package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// UserStatusPending 已注册未邮箱验证：仅邮箱注册路径的默认态，本地账号不经过
const UserStatusPending = "pending"

// UserStatusActive 正常可用：邮箱验证通过后，或本地账号注册即激活
const UserStatusActive = "active"

// UserStatusSuspended 被管理员封禁：access 立即失效，refresh 只能换来封禁提示
const UserStatusSuspended = "suspended"

// UserStatusDeleted 软删除：身份查档保留，对外不可见
const UserStatusDeleted = "deleted"

// User 用户身份本体，「你是谁」的稳定载体
type User struct {
	ent.Schema
}

// Fields 返回 User 的字段定义
func (User) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Immutable().
			Comment("主键，全局稳定身份"),
		field.String("email").
			Optional().
			Nillable().
			Comment("联系邮箱，可空（未绑定时 NULL），登录凭据在 identities，本列仅展示与找回"),
		field.String("username").
			Unique().
			Comment("用户名句柄，本地账号登录标识，3-20 位字母数字 _ -"),
		field.String("display_name").
			Comment("展示昵称，可随时修改且允许重名"),
		field.String("avatar_url").
			Optional().
			Nillable().
			Comment("头像地址，可空"),
		field.Enum("status").
			Values(UserStatusPending, UserStatusActive, UserStatusSuspended, UserStatusDeleted).
			Default(UserStatusPending).
			Comment("账户状态机：pending / active / suspended / deleted"),
		field.Int("token_version").
			Default(0).
			Comment("令牌版本，封禁/改密/重置时 +1，使旧 access 失效的关键"),
		field.String("password_hash").
			Sensitive().
			Comment("账户主凭据的 argon2id 哈希，local 与 email 登录共用同一份密码"),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			Comment("创建时间"),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			Comment("最近更新时间"),
		field.Time("deleted_at").
			Optional().
			Nillable().
			Comment("软删除时间，可空"),
	}
}

// Edges 返回 User 的出边：一个用户持有多个身份凭据 / 会话 / 一次性令牌，可主多个房间、出现在多个房间
func (User) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("identities", Identity.Type).Comment("该用户的登录凭据集合"),
		edge.To("sessions", Session.Type).Comment("该用户的会话记录集合"),
		edge.To("auth_tokens", AuthToken.Type).Comment("该用户的一次性短命令牌集合"),
		edge.To("hosted_rooms", Room.Type).Comment("该用户担任房主的房间集合"),
		edge.To("room_memberships", RoomMember.Type).Comment("该用户的房间在场记录集合"),
	}
}

// Indexes 声明 email 的部分唯一索引：仅为非 NULL 的行建唯一约束，允许多个无邮箱本地账号共存
func (User) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("email").
			Unique().
			Annotations(entsql.IndexWhere("email IS NOT NULL")),
	}
}