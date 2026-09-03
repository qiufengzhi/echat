// e2e_test.go SpiceDB 端到端验证：需本地 dev compose 已起 spicedb，否则自动跳过
package authz_test

import (
	"context"
	"os"
	"testing"
	"time"

	"echat-backend/authz"
)

// TestE2E 覆盖角色分配、权限派生、静音覆盖与房主交接的真实 SpiceDB 链路
// 依赖外部服务：未起容器时 t.Skip 跳过，不作为单元测试阻塞
func TestE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	addr := os.Getenv("SPICEDB_GRPC_ADDR")
	if addr == "" {
		addr = "127.0.0.1:50051"
	}
	c, err := authz.NewClient(addr, "dev-secret-local")
	if err != nil {
		t.Skipf("SpiceDB 客户端创建失败，跳过 e2e: %v", err)
	}
	// schema 幂等写入（开发期测试也保幂等语义）
	if err = c.EnsureSchema(ctx); err != nil {
		t.Skipf("SpiceDB schema 写入失败，跳过 e2e: %v", err)
	}

	room := "test-room-a"
	host := "test-user-host"
	coworker := "test-user-cohost"
	audience := "test-user-listener"
	outsider := "test-user-outsider"

	// 清场：保证用例幂等可重跑
	for _, u := range []string{host, coworker, audience, outsider} {
		for _, r := range []string{"host", "cohost", "speaker", "listener", "muted"} {
			_ = c.RemoveRoomRole(ctx, r, room, u)
		}
	}

	// 房主 + cohost + listener（普通听众）
	mustNoErr(t, c.AssignRoomRole(ctx, "host", room, host))
	mustNoErr(t, c.AssignRoomRole(ctx, "cohost", room, coworker))
	mustNoErr(t, c.AssignRoomRole(ctx, "listener", room, audience))

	// 房主持有全部管理权限，cohost 继承一半，听众只可听不可管
	must(t, c, ctx, true, "host 可编辑房间", "edit_room", room, host)
	must(t, c, ctx, true, "host 可踢人", "kick", room, host)
	must(t, c, ctx, true, "cohost 可管 AI", "manage_ai", room, coworker)
	must(t, c, ctx, true, "cohost 可踢人", "kick", room, coworker)
	must(t, c, ctx, false, "listener 不可踢人", "kick", room, audience)
	must(t, c, ctx, true, "listener 可听", "listen", room, audience)
	must(t, c, ctx, false, "未入房者不可听", "listen", room, outsider)

	// 升麦后 speak 成立，静音覆盖 speak 不覆盖成员身份
	mustNoErr(t, c.AssignRoomRole(ctx, "speaker", room, audience))
	must(t, c, ctx, true, "speaker 可发言", "speak", room, audience)
	must(t, c, ctx, true, "被静音前仍为成员", "listen", room, audience)
	mustNoErr(t, c.AssignRoomRole(ctx, "muted", room, audience))
	must(t, c, ctx, false, "静音后不可发言", "speak", room, audience)
	must(t, c, ctx, true, "静音仍是成员", "listen", room, audience)
	mustNoErr(t, c.RemoveRoomRole(ctx, "muted", room, audience))
	must(t, c, ctx, true, "解除静音恢复发言", "speak", room, audience)

	// 房主交接：单请求内删旧写新，任意时刻仅一房主
	mustNoErr(t, c.TransferHost(ctx, room, host, coworker))
	must(t, c, ctx, true, "交接后新 host 生效", "host", room, coworker)
	must(t, c, ctx, false, "旧 host 失去交接权", "transfer_host", room, host)
}

// must 断言 Can 判定结果与期望一致，判定出错立即失败
func must(t *testing.T, c *authz.Client, ctx context.Context, want bool, why, permission, room, user string) {
	t.Helper()
	ok, err := c.Can(ctx, permission, room, user)
	if err != nil {
		t.Fatalf("判定出错（%s）: %v", why, err)
	}
	if ok != want {
		t.Fatalf("权限 %s 结果=%v 期望=%v（%s）", permission, ok, want, why)
	}
}

// mustNoErr 断言无错误，带用例名快速定位
func mustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}