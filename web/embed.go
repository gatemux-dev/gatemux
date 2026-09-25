package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var assets embed.FS

// Assets returns the SPA build output as a virtual filesystem rooted at dist/.
// Build the SPA with `npm run build` in web/ before `go build`.
func Assets() (fs.FS, error) {
	return fs.Sub(assets, "dist")
}
