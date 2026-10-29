// Package migrations 将 SQL 迁移文件打包进 Go 二进制，
// 初始化与离线升级因此不依赖运行目录中存在这些 SQL 文件。
package migrations

import "embed"

// Files 提供嵌入二进制的 SQL 文件系统；执行顺序由 tracking.MigrationNames 指定，不靠遍历顺序。
// 下方 go:embed 是编译指令，不能翻译、随意断开或当普通说明删除；*.sql 相对于本包目录匹配。
//
//go:embed *.sql
var Files embed.FS
