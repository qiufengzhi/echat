// roles.go 关系元组写入：角色分配/撤销与房主交接
//
// 写路径全部幂等（重放同一关系结果相同），角色变更用「关系集合差」表达，避免重复元组
package authz

import (
	"context"

	"github.com/authzed/authzed-go/proto/authzed/api/v1"
)

// AssignRoomRole 写入一条角色关系元组（TOUCH 幂等：已存在则结果不变）
// role 支持 host/cohost/speaker/listener/muted，roomID 与 userID 为 SpiceDB 对象 ID
func (c *Client) AssignRoomRole(ctx context.Context, role, roomID, userID string) error {
	_, err := c.raw.WriteRelationships(ctx, &v1.WriteRelationshipsRequest{
		Updates: []*v1.RelationshipUpdate{{
			Operation:    v1.RelationshipUpdate_OPERATION_TOUCH,
			Relationship: roomRelationship(role, roomID, userID),
		}},
	})
	if err == nil {
		c.cache.invalidateRoom(roomID) // 角色可变，房间判定缓存整体失效防止读旧
	}
	return err
}

// RemoveRoomRole 删除一条角色关系元组（幂等：不存在也视为成功）
func (c *Client) RemoveRoomRole(ctx context.Context, role, roomID, userID string) error {
	_, err := c.raw.WriteRelationships(ctx, &v1.WriteRelationshipsRequest{
		Updates: []*v1.RelationshipUpdate{{
			Operation:    v1.RelationshipUpdate_OPERATION_DELETE,
			Relationship: roomRelationship(role, roomID, userID),
		}},
	})
	if err == nil {
		c.cache.invalidateRoom(roomID)
	}
	return err
}

// TransferHost 房主交接：一次请求内删旧 host 写新 host，保证任意时刻恰好一个 host，天然防双房主
func (c *Client) TransferHost(ctx context.Context, roomID, from, to string) error {
	_, err := c.raw.WriteRelationships(ctx, &v1.WriteRelationshipsRequest{
		Updates: []*v1.RelationshipUpdate{
			{Operation: v1.RelationshipUpdate_OPERATION_DELETE, Relationship: roomRelationship("host", roomID, from)},
			{Operation: v1.RelationshipUpdate_OPERATION_TOUCH, Relationship: roomRelationship("host", roomID, to)},
		},
	})
	if err == nil {
		c.cache.invalidateRoom(roomID)
	}
	return err
}

// roomRelationship 构造 user 与 room 之间的角色关系元组
// role 为关系名，roomID/userID 为对象 ID
func roomRelationship(role, roomID, userID string) *v1.Relationship {
	return &v1.Relationship{
		Resource: &v1.ObjectReference{ObjectType: TypeRoom, ObjectId: roomID},
		Relation: role,
		Subject:  &v1.SubjectReference{Object: &v1.ObjectReference{ObjectType: TypeUser, ObjectId: userID}},
	}
}