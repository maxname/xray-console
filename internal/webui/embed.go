// Package webui carries the built frontend inside the binary.
package webui

import (
	"embed"
	"io/fs"
)

// dist is written by `pnpm build` in ../../web. The .gitkeep keeps the
// directory present in a fresh checkout so this still compiles before the
// frontend has been built.
//
//go:embed all:dist
var dist embed.FS

// FS returns the built frontend rooted at its index.html.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // dist is embedded at compile time; this cannot fail at run time.
	}
	return sub
}
