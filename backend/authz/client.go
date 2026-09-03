// Package authz SpiceDB(Zanzibar) 对象级授权服务封装
//
// 定位：授权/权限裁决只走控制面（kick / manage_ai / mod_mic / speak 校验 / 交接）；
// 媒体路径（音频包、SFU 转发）永不查 SpiceDB，权限在「能不能开口」收口
// 关系元组写入天然幂等（TOUCH/DELETE 结果相同），是「写隔离系统」的持久真相源
package authz

import (
	"context"
	_ "embed"
	"time"

	"github.com/authzed/authzed-go/proto/authzed/api/v1"
	authzed "github.com/authzed/authzed-go/v1"
	"github.com/authzed/grpcutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TypeRoom SpiceDB 对象类型：房间，关系与权限均挂在 room 定义下
const TypeRoom = "room"

// TypeUser SpiceDB 对象类型：用户（subject 端）
const TypeUser = "user"

//go:embed schema.zed
var schemaFile string

// Client SpiceDB 服务封装：启动水合 schema，运行时提供权限判定与关系写入
type Client struct {
	// raw 底层 gRPC 客户端，封装 SpiceDB v1 API
	raw *authzed.Client
	// cache 判定结果秒级 TTL 缓存，高热房间裁决短期内不变
	cache *checkCache
}

// NewClient 建立到 SpiceDB 的 gRPC 连接并返回服务封装
// addr 形如 "127.0.0.1:50051"，token 为服务端配置的 preshared key（dev 用 insecure 明文传输）
func NewClient(addr, token string) (*Client, error) {
	raw, err := authzed.NewClient(
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpcutil.WithInsecureBearerToken(token),
	)
	if err != nil {
		return nil, err
	}
	return &Client{raw: raw, cache: newCheckCache(5 * time.Second)}, nil
}

// Close 释放底层 gRPC 连接
func (c *Client) Close() error {
	return c.raw.Close()
}

// EnsureSchema 幂等写入 schema.zed（开发期启动时 upsert；生产由审批流/CLI 预置保持一致）
func (c *Client) EnsureSchema(ctx context.Context) error {
	_, err := c.raw.WriteSchema(ctx, &v1.WriteSchemaRequest{Schema: schemaFile})
	return err
}

// Ping 探测 SpiceDB 连通性，未连接即返回错误
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.raw.ReadSchema(ctx, &v1.ReadSchemaRequest{})
	return err
}