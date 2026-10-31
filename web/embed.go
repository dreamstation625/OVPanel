// Package web 内嵌 Vite 构建产物，使发布后的面板只需一个 Go 二进制。
//
// 开发时先在 web 目录执行 npm ci && npm run build；运行期不需要 Node.js。
// dist 目录中的 .gitkeep 保证尚未构建前端时 go build 也能通过。
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var assets embed.FS

// Assets 返回以 dist 为根的只读文件系统。
func Assets() fs.FS {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		// embed 目录在编译期已确定，只有目录缺失才会走到这里。
		panic("web: 内嵌资源目录缺失: " + err.Error())
	}
	return sub
}
