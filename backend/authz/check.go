// check.go 权限判定与进程内缓存
package authz

import (
	"context"
	"sync"
	"time"

	"github.com/authzed/authzed-go/proto/authzed/api/v1"
	"github.com/hashicorp/golang-lru/v2"
)

// Can 判定 user 对房间某权限是否成立（权限名如 kick / speak / manage_ai）
// 结果带秒级 TTL 缓存返回，避免高热房间重复走 gRPC 图遍历
func (c *Client) Can(ctx context.Context, permission, roomID, userID string) (bool, error) {
	key := permission + "|" + roomID + "|" + userID
	if e, ok := c.cache.get(key); ok {
		return e, nil
	}
	resp, err := c.raw.CheckPermission(ctx, &v1.CheckPermissionRequest{
		Resource:   &v1.ObjectReference{ObjectType: TypeRoom, ObjectId: roomID},
		Permission: permission,
		Subject:    &v1.SubjectReference{Object: &v1.ObjectReference{ObjectType: TypeUser, ObjectId: userID}},
		// 强一致：控制面鉴权必须读到刚写入的角色（默认 minimize-latency 会读旧快照，角色刚变更会误判）
		Consistency: &v1.Consistency{Requirement: &v1.Consistency_FullyConsistent{FullyConsistent: true}},
	})
	if err != nil {
		return false, err
	}
	allowed := resp.Permissionship == v1.CheckPermissionResponse_PERMISSIONSHIP_HAS_PERMISSION
	c.cache.set(key, roomID, allowed)
	return allowed, nil
}

// checkCache 进程内 LRU + TTL：命中免走网络，过期自动回源并回填
type checkCache struct {
	// lru 原子安全的最近最少使用缓存
	lru *lru.Cache[string, checkEntry]
	// mu 保护 idx 反向索引的并发访问
	mu sync.RWMutex
	// idx room -> 该房间已缓存的 key 集合，关系写入后按房间批量失效
	idx map[string]map[string]struct{}
	// ttl 单条裁决的有效期（控制面操作容忍秒级误差）
	ttl time.Duration
}

// checkEntry 一条缓存裁决
type checkEntry struct {
	// allowed 是否拥有权限
	allowed bool
	// expires 失效时刻
	expires time.Time
}

// newCheckCache 构造限长 1024 条、ttl 秒级过期的判定缓存
func newCheckCache(ttl time.Duration) *checkCache {
	c, _ := lru.New[string, checkEntry](1024)
	return &checkCache{lru: c, idx: make(map[string]map[string]struct{}), ttl: ttl}
}

// get 返回缓存命中且未过期的裁决
// key 三元组标识，返回是否有命中与裁决结果
func (cc *checkCache) get(key string) (bool, bool) {
	e, ok := cc.lru.Get(key)
	if !ok || time.Now().After(e.expires) {
		return false, false
	}
	return e.allowed, true
}

// set 写入一条裁决、刷新过期时刻并登记 room 反向索引
func (cc *checkCache) set(key, room string, allowed bool) {
	cc.lru.Add(key, checkEntry{allowed: allowed, expires: time.Now().Add(cc.ttl)})
	cc.mu.Lock()
	m := cc.idx[room]
	if m == nil {
		m = make(map[string]struct{})
		cc.idx[room] = m
	}
	m[key] = struct{}{}
	cc.mu.Unlock()
}

// invalidateRoom 关系写入后清除该房间全部判定缓存，保证下次 Can 回源读最新角色
func (cc *checkCache) invalidateRoom(room string) {
	cc.mu.Lock()
	m := cc.idx[room]
	delete(cc.idx, room)
	cc.mu.Unlock()
	for key := range m {
		cc.lru.Remove(key)
	}
}