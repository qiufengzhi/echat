// apply.go 落地 migrate 子命令：把内嵌的版本化迁移 apply 到目标库
//
// 方案 A 的关键：迁移 SQL 随二进制/镜像内嵌，apply 与运行时 store.Migrate 的漂移校验同源，
// 结构上消除「迁移目录版本 ≠ 二进制内嵌版本」这类漂移（对齐 docs/deploy-config-repo.md §7）
package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/postgres"
	_ "github.com/jackc/pgx/v5/stdlib" // 注册 database/sql 驱动名 "pgx"
)

// revisionsSchema 迁移日记表所在 schema，与 Atlas CLI --revisions-schema 口径一致
const revisionsSchema = "atlas_schema_revisions"

// revisionsTable 迁移日记表名，与 Atlas CLI 默认一致，store.Migrate 亦查此表
const revisionsTable = "atlas_schema_revisions"

// Apply 把内嵌迁移 apply 到 dsn 指向的库，幂等：已是最新则无操作
//
// ctx 控制整体超时，dsn 为 postgres URL（postgres://user:pass@host:port/db?sslmode=disable）
// 返回 nil 表示库与内嵌迁移一致；打开连接/执行/复核任一步失败返回错误
func Apply(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("打开数据库: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("连接数据库: %w", err)
	}

	// 复用标准库连接作为 Atlas 驱动：postgres.Open 只做包裹，连接由上层 db 持有
	drv, err := postgres.Open(db)
	if err != nil {
		return fmt.Errorf("初始化 postgres 驱动: %w", err)
	}

	rrw := &revisions{db: db}
	if err := rrw.ensure(ctx); err != nil {
		return err
	}

	dir, err := memDir()
	if err != nil {
		return err
	}
	defer dir.Close()

	ex, err := migrate.NewExecutor(drv, dir, rrw)
	if err != nil {
		return fmt.Errorf("构造迁移执行器: %w", err)
	}
	// 0 = 应用全部待执行迁移；库已是最新时 Atlas 返回 ErrNoPendingFiles，视为幂等成功
	if err := ex.ExecuteN(ctx, 0); err != nil && !errors.Is(err, migrate.ErrNoPendingFiles) {
		return fmt.Errorf("执行迁移: %w", err)
	}
	return verify(ctx, db)
}

// memDir 把内嵌的迁移 SQL 与 atlas.sum 装进内存目录，供 Atlas 执行器读取
//
// 返回的内存目录含全部 *.sql 与 atlas.sum（缺 sum 时 Atlas 校验会报 ErrChecksumNotFound）
func memDir() (*migrate.MemDir, error) {
	dir := migrate.OpenMemDir("echat")
	entries, err := FS.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("读取内嵌迁移目录: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := FS.ReadFile(e.Name())
		if err != nil {
			return nil, fmt.Errorf("读取内嵌迁移文件 %s: %w", e.Name(), err)
		}
		if err := dir.WriteFile(e.Name(), b); err != nil {
			return nil, fmt.Errorf("写入内存迁移目录 %s: %w", e.Name(), err)
		}
	}
	return dir, nil
}

// verify 复核「库内已应用最新版本」等于「内嵌最新版本」，口径与 store.Migrate 一致
//
// ctx 控制查询超时，db 为已连接句柄
// 返回 nil 表示一致；不一致返回带双方版本号的错误，避免 apply 静默成功但 schema 仍漂移
func verify(ctx context.Context, db *sql.DB) error {
	latest, err := latestEmbedded()
	if err != nil {
		return err
	}
	var applied sql.NullString
	err = db.QueryRowContext(ctx,
		`SELECT version FROM `+revisionsSchema+`.`+revisionsTable+
			` WHERE type = 2 ORDER BY executed_at DESC LIMIT 1`).Scan(&applied)
	if err != nil {
		return fmt.Errorf("读取迁移日记表: %w", err)
	}
	if applied.String != latest {
		return fmt.Errorf("迁移复核失败: 库内已应用 %q，内嵌最新 %q", applied.String, latest)
	}
	return nil
}

// latestEmbedded 返回内嵌迁移目录中最新迁移文件的版本号
//
// Atlas 命名规范 {version}_{description}.sql，version 为第一个下划线前段
// 返回最新版本号；目录为空或读取失败返回错误
func latestEmbedded() (string, error) {
	entries, err := FS.ReadDir(".")
	if err != nil {
		return "", fmt.Errorf("读取内嵌迁移目录: %w", err)
	}
	var latest string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || len(name) < 5 || name[len(name)-4:] != ".sql" {
			continue
		}
		base := name[:len(name)-4]
		if i := indexByte(base, '_'); i > 0 {
			base = base[:i]
		}
		if base > latest {
			latest = base
		}
	}
	if latest == "" {
		return "", fmt.Errorf("内嵌迁移目录中没有版本化 SQL 文件")
	}
	return latest, nil
}

// indexByte 返回 c 在 s 中首次出现的下标，未找到返回 -1
func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// revisions 基于 *sql.DB 实现 migrate.RevisionReadWriter，读写 atlas_schema_revisions.atlas_schema_revisions
//
// 表结构、列名、type=2 语义均与 Atlas CLI 保持一致，保证与既有线上库兼容
type revisions struct {
	db *sql.DB // 迁移日记读写所用的标准库连接
}

// Ident 返回日记表的标识（schema + 表名）
func (r *revisions) Ident() *migrate.TableIdent {
	return &migrate.TableIdent{Name: revisionsTable, Schema: revisionsSchema}
}

// ensure 幂等准备日记 schema 与表，兼容全新库（CLI 不来建表时）
//
// ctx 控制建表超时
// 返回 nil 表示 schema 与表已就绪
func (r *revisions) ensure(ctx context.Context) error {
	stmts := []string{
		`CREATE SCHEMA IF NOT EXISTS ` + revisionsSchema,
		`CREATE TABLE IF NOT EXISTS ` + revisionsSchema + `.` + revisionsTable + ` (
			version          text PRIMARY KEY,
			description      text NOT NULL DEFAULT '',
			type             bigint NOT NULL DEFAULT 2,
			applied          bigint NOT NULL DEFAULT 0,
			total            bigint NOT NULL DEFAULT 0,
			executed_at      timestamptz NOT NULL DEFAULT now(),
			execution_time   bigint NOT NULL DEFAULT 0,
			error            text NOT NULL DEFAULT '',
			error_stmt       text NOT NULL DEFAULT '',
			hash             text NOT NULL,
			partial_hashes   jsonb NOT NULL DEFAULT '[]',
			operator_version text NOT NULL DEFAULT ''
		)`,
	}
	for _, s := range stmts {
		if _, err := r.db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("准备迁移日记表: %w", err)
		}
	}
	return nil
}

// ReadRevisions 返回全部迁移记录，按版本升序
//
// ctx 控制查询超时
// 返回全量记录；查询或解析失败返回错误
func (r *revisions) ReadRevisions(ctx context.Context) ([]*migrate.Revision, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+revisionCols+` FROM `+revisionsSchema+`.`+revisionsTable+` ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*migrate.Revision
	for rows.Next() {
		rev, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rev)
	}
	return out, rows.Err()
}

// ReadRevision 按版本返回单条记录
//
// ctx 控制查询超时，version 为迁移版本号
// 命中返回记录；未命中返回 migrate.ErrRevisionNotExist
func (r *revisions) ReadRevision(ctx context.Context, version string) (*migrate.Revision, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+revisionCols+` FROM `+revisionsSchema+`.`+revisionsTable+` WHERE version = $1`, version)
	rev, err := scanRevision(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, migrate.ErrRevisionNotExist
	}
	if err != nil {
		return nil, err
	}
	return rev, nil
}

// WriteRevision 写入或按 version 更新一条记录（upsert）
//
// ctx 控制写入超时，rev 为待落库的迁移记录
// 返回 nil 表示落库成功
func (r *revisions) WriteRevision(ctx context.Context, rev *migrate.Revision) error {
	ph, err := json.Marshal(rev.PartialHashes)
	if err != nil {
		return fmt.Errorf("编码 partial_hashes: %w", err)
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO `+revisionsSchema+`.`+revisionsTable+` (`+revisionCols+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12)
		 ON CONFLICT (version) DO UPDATE SET
		   description = EXCLUDED.description, type = EXCLUDED.type,
		   applied = EXCLUDED.applied, total = EXCLUDED.total,
		   executed_at = EXCLUDED.executed_at, execution_time = EXCLUDED.execution_time,
		   error = EXCLUDED.error, error_stmt = EXCLUDED.error_stmt,
		   hash = EXCLUDED.hash, partial_hashes = EXCLUDED.partial_hashes,
		   operator_version = EXCLUDED.operator_version`,
		rev.Version, rev.Description, int(rev.Type), rev.Applied, rev.Total,
		rev.ExecutedAt, rev.ExecutionTime.Nanoseconds(), rev.Error, rev.ErrorStmt,
		rev.Hash, string(ph), rev.OperatorVersion)
	return err
}

// DeleteRevision 按版本删除一条记录
//
// ctx 控制删除超时，version 为迁移版本号
// 返回 nil 表示删除执行完成（记录不存在也算成功）
func (r *revisions) DeleteRevision(ctx context.Context, version string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM `+revisionsSchema+`.`+revisionsTable+` WHERE version = $1`, version)
	return err
}

// revisionCols 日记表的列清单，读（SELECT）与写（INSERT）共用，保证两侧列序一致
const revisionCols = `version, description, type, applied, total, executed_at, execution_time, error, error_stmt, hash, partial_hashes, operator_version`

// rowScanner 抽象 *sql.Row 与 *sql.Rows 的 Scan 能力
type rowScanner interface {
	// Scan 将当前行各列扫描进给定目标
	Scan(dest ...any) error
}

// scanRevision 把一行日记记录扫描成 migrate.Revision
//
// src 为单行扫描源
// 返回解析后的记录；jsonb 解析或类型不匹配返回错误
func scanRevision(src rowScanner) (*migrate.Revision, error) {
	var (
		rev   migrate.Revision
		typ   int64
		phRaw []byte
	)
	if err := src.Scan(&rev.Version, &rev.Description, &typ, &rev.Applied, &rev.Total,
		&rev.ExecutedAt, &rev.ExecutionTime, &rev.Error, &rev.ErrorStmt,
		&rev.Hash, &phRaw, &rev.OperatorVersion); err != nil {
		return nil, err
	}
	rev.Type = migrate.RevisionType(typ)
	if len(phRaw) > 0 {
		if err := json.Unmarshal(phRaw, &rev.PartialHashes); err != nil {
			return nil, fmt.Errorf("解析 partial_hashes: %w", err)
		}
	}
	return &rev, nil
}
