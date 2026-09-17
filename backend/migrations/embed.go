// Package migrations 内嵌 Atlas 版本化迁移 SQL
//
// 迁移文件在编译期打进二进制，运行时不再依赖当前工作目录或镜像里是否存在
// migrations 目录，避免容器/systemd 下路径漂移导致启动装配失败。
// store.Migrate 只做「代码 schema 与 DB 日记表」漂移校验，因此仅需文件名列表。
package migrations

import "embed"

// FS 暴露本目录下所有版本化迁移 SQL（文件名形如 20260903_000000_initial.sql）
//
//go:embed *.sql
var FS embed.FS
