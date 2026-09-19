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
	"errors"
	"fmt"
	"strings"
	"time"

	"echat-backend/config"
	"echat-backend/ent"
	"echat-backend/migrations"

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
		return nil, fmt.Errorf("PostgreSQL 连接失败(%s:%d/%s)—: %w",
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

// revisionsTable Atlas 迁移日记表（新版本放于同名 schema，旧版为 public.atlas_schema_migrations）
const revisionsTable = "atlas_schema_revisions.atlas_schema_revisions"

// Migrate 校验版本化迁移已应用到最新，防「代码 schema 与 DB 漂移」
// schema 演进交由 backend/migrations 版本化 SQL 管理（Atlas 格式，日记表 atlas_schema_revisions），
// 由 dev compose 的 migrate 服务执行 apply；启动期只校验，未应用/落后均返回错误并提示迁移命令
func (s *Store) Migrate(ctx context.Context) error {
	latest, err := latestMigrationVersion()
	if err != nil {
		return err
	}
	var applied string
	err = s.std.QueryRowContext(ctx,
		`SELECT version FROM `+revisionsTable+` WHERE type = 2 ORDER BY executed_at DESC LIMIT 1`).Scan(&applied)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("数据库尚未应用版本化迁移——请先运行: docker compose -f deploy/docker-compose.dev.yml run --rm migrate")
	case err != nil:
		return fmt.Errorf("读取迁移日记表 %s: %w", revisionsTable, err)
	}
	if applied != latest {
		return fmt.Errorf("数据库已应用迁移 %s 落后于最新 %s，schema 漂移——请运行迁移服务后重启", applied, latest)
	}
	return nil
}

// latestMigrationVersion 返回内嵌迁移目录中最新迁移文件的版本号
// 迁移文件由 backend/migrations 编译期内嵌进二进制，运行时不再依赖磁盘上的迁移目录
// Atlas 迁移文件命名规范 {version}_{description}.sql，version 为第一个下划线前段（如 20260903）
func latestMigrationVersion() (string, error) {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return "", fmt.Errorf("读取内嵌迁移目录: %w", err)
	}
	var latest string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".sql")
		version := base
		if i := strings.Index(base, "_"); i > 0 {
			version = base[:i]
		}
		if version > latest {
			latest = version
		}
	}
	if latest == "" {
		return "", fmt.Errorf("内嵌迁移目录中没有版本化 SQL 文件")
	}
	return latest, nil
}

// Close 关闭连接池，进程退出前应 defer 调用
func (s *Store) Close() {
	s.pool.Close()
	s.std.Close()
}
