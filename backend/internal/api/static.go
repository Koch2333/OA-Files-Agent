package api

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// static serves the built Vue app. Unknown paths fall back to index.html so client-side routes survive a reload.
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	root := s.d.StaticDir
	clean := path.Clean("/" + r.URL.Path) // rooted Clean removes any ".." before the join
	full := filepath.Join(root, filepath.FromSlash(clean))
	if fi, err := os.Stat(full); err == nil && !fi.IsDir() {
		if strings.HasPrefix(clean, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.ServeFile(w, r, full)
		return
	}
	if path.Ext(clean) != "" && !strings.HasSuffix(clean, ".html") { // missing asset: real 404, not the SPA shell
		s.fail(w, r, 404, "NOT_FOUND", "文件不存在", nil)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, filepath.Join(root, "index.html"))
}
