package web

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
)

//go:embed all:static
var embeddedStatic embed.FS

// StaticFS returns the dashboard's web/static files. The first of dirs that
// exists as a directory is served from disk, so the UI can be edited without
// rebuilding; otherwise the copy embedded in the binary is used. The returned
// string names the source for logging.
func StaticFS(dirs ...string) (http.FileSystem, string) {
	for _, dir := range dirs {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return http.Dir(dir), dir
		}
	}
	sub, _ := fs.Sub(embeddedStatic, "static")
	return http.FS(sub), "embedded"
}
