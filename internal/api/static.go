package api

import (
	"io"
	"net/http"
	"path"
	"strings"
)

// staticHandler 提供内嵌前端资源，并对未知路径回退到 index.html。
//
// 单页应用的路由由前端处理，服务端只需要返回入口页；
// 但带扩展名的静态资源缺失时必须返回 404，不能回退成 HTML，
// 否则浏览器会把 404 的 JS 当成脚本执行。
func (s *Server) staticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, r, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "请求方法不被支持")
			return
		}
		if s.webFS == nil {
			http.NotFound(w, r)
			return
		}

		clean := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
		name := strings.TrimPrefix(clean, "/")
		if name == "" || name == "." {
			name = "index.html"
		}

		if f, err := s.webFS.Open(name); err == nil {
			f.Close()
			s.serveFile(w, r, name)
			return
		}

		// 静态资源缺失直接 404；其余路径交给前端路由。
		if hasFileExtension(name) {
			if name == "index.html" {
				http.Error(w, "前端资源尚未构建：在 web 目录执行 npm ci && npm run build", http.StatusServiceUnavailable)
				return
			}
			http.NotFound(w, r)
			return
		}

		if f, err := s.webFS.Open("index.html"); err != nil {
			http.Error(w, "前端资源尚未构建：在 web 目录执行 npm ci && npm run build", http.StatusServiceUnavailable)
			return
		} else {
			f.Close()
		}
		s.serveFile(w, r, "index.html")
	})
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, name string) {
	f, err := s.webFS.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	content, err := io.ReadAll(f)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", contentType(name))
	// 入口页和未带内容哈希的资源不缓存，保证升级后能拿到新版本；
	// Vite 在 assets/ 下生成的 name-hash.ext 才可以长期缓存。
	if isHashedAsset(name) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	_, _ = w.Write(content)
}

// isHashedAsset 判断是否为 Vite 生成的内容哈希资源，例如 assets/index-a1b2c3d4.js。
func isHashedAsset(name string) bool {
	if !strings.HasPrefix(name, "assets/") {
		return false
	}
	base := path.Base(name)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	dash := strings.LastIndex(stem, "-")
	if dash < 1 || len(stem)-dash-1 < 8 {
		return false
	}
	for _, c := range stem[dash+1:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func hasFileExtension(name string) bool {
	base := path.Base(name)
	idx := strings.LastIndex(base, ".")
	return idx > 0
}

func contentType(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".woff2":
		return "font/woff2"
	case ".ico":
		return "image/x-icon"
	default:
		return "application/octet-stream"
	}
}
