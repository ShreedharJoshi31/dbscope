// Package web embeds the built UI (web/dist) into the dbscope binary.
package web

import (
	"embed"
	"io/fs"
)

// dist always contains .gitkeep (recreated by the Vite build), so the embed
// compiles in a fresh clone before the UI has been built.
//
//go:embed all:dist
var dist embed.FS

// Assets returns the built UI, or nil when the binary was built without it.
func Assets() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // constant, valid path
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	return sub
}
