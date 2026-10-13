package migrations

import (
	"context"
	"os"
	"testing"

	"ariga.io/atlas/sql/migrate"
)

// 迁移目录必须能通过 Atlas 的 checksum 校验——atlas.sum 已内嵌，缺它 Validate 会报 ErrChecksumNotFound
func TestMemDirChecksumValid(t *testing.T) {
	dir, err := memDir()
	if err != nil {
		t.Fatalf("构造内存迁移目录失败 %v", err)
	}
	defer dir.Close()
	if err := migrate.Validate(dir); err != nil {
		t.Fatalf("atlas.sum 校验失败 %v", err)
	}
	files, err := dir.Files()
	if err != nil {
		t.Fatalf("列举迁移文件失败 %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("内存目录为空，预期含版本化 SQL")
	}
}

// 内嵌目录必须解析出非空最新版本号
func TestLatestEmbeddedNonEmpty(t *testing.T) {
	v, err := latestEmbedded()
	if err != nil {
		t.Fatalf("latestEmbedded 失败 %v", err)
	}
	if v == "" {
		t.Fatalf("latestEmbedded 返回空版本号")
	}
}

// 真机集成测试：仅当 MIGRATE_TEST_DSN 指向一个可用空库时执行（CI 不设此变量，自动跳过）
// 覆盖 apply 全链路：建日记表 → 执行内嵌迁移 → 复核；并验证第二次调用为幂等 no-op
func TestApplyAgainstRealDB(t *testing.T) {
	dsn := os.Getenv("MIGRATE_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 MIGRATE_TEST_DSN，跳过真机迁移测试")
	}
	ctx := context.Background()
	if err := Apply(ctx, dsn); err != nil {
		t.Fatalf("首次 apply 失败 %v", err)
	}
	if err := Apply(ctx, dsn); err != nil {
		t.Fatalf("第二次 apply 应为幂等 no-op，却失败 %v", err)
	}
}
