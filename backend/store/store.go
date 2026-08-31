// Package store 统一管理 PostgreSQL 持久层的连接与访问通道
//
// 连接策略：一个 pgx 连接池同时服务于两类访问——
//   - pgx 原生查询（事务性 Outbox 等需要手写 SQL 的场景）
//   - 由同一池派生的 *sql.DB，包装成 Ent driver（业务读写走 ORM）
//
// 单池双通道的好处：所有连接复用同一池，池内协程数可控，读路径与写路径共享连接预算
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"echat-backend/config"
	"echat-backend/ent"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// Store PostgreSQL 访问门面，持有唯一连接池与派生出的 Ent 客户端
type Store struct {
	pool *pgxpool.Pool // pgx 原生连接池
	std  *sql.DB       // 同一池派生的标准库句柄，供 Ent driver 使用
	ent  *ent.Client   // 基于 std 的 Ent ORM 客户端
}

// Open 建立连接池、构建 Ent 客户端，并校验连通性
// ctx 用于初始化建连，cfg 由 config.Database 提供
// 返回的 *Store 已在启动时完成一次 Ping，失败返回错误
func Open(ctx context.Context, cfg config.DatabaseConfig) (*Store, error) {
	pool, err := pgxpool.New(ctx, cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("创建 pgx 连接池: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("PostgreSQL 连接失败(%s:%d/%s)——请先运行 docker compose -f deploy/docker-compose.dev.yml up -d: %w",
			cfg.Host, cfg.Port, cfg.Name, err)
	}

	db := stdlib.OpenDBFromPool(pool)
	drv := entsql.OpenDB(dialect.Postgres, db)
	return &Store{
		pool: pool,
		std:  db,
		ent:  ent.NewClient(ent.Driver(drv)),
	}, nil
}

// Pool 返回 pgx 原生连接池
func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
}

// StdDB 返回由连接池派生的标准库 *sql.DB
func (s *Store) StdDB() *sql.DB {
	return s.std
}

// Ent 返回 Ent ORM 客户端，业务读写通过它进行
func (s *Store) Ent() *ent.Client {
	return s.ent
}

// Migrate 按当前 Schema 自动建表/变更表结构（开发期使用）
// 生产环境的迁移由 Atlas 生成版本化 SQL 文件管理，此处仅为本地开发提供便利
func (s *Store) Migrate(ctx context.Context) error {
	if err := s.ent.Schema.Create(ctx); err != nil {
		return fmt.Errorf("执行开发期 schema 迁移: %w", err)
	}
	return nil
}

// Close 关闭连接池，进程退出前应 defer 调用
func (s *Store) Close() {
	s.pool.Close()
	s.std.Close()
}