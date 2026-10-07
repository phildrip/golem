package server

import (
	"embed"
	"io/fs"
)

// EmbeddedUIFS contains the embedded SvelteKit production build assets.
// When built with embedded SPA, ui/build is bundled directly into the Go binary.
//
//go:embed all:ui_dist
var embeddedDist embed.FS

// GetEmbeddedUIFS returns the embedded file system for SPA hosting if present.
func GetEmbeddedUIFS() fs.FS {
	sub, err := fs.Sub(embeddedDist, "ui_dist")
	if err != nil {
		return nil
	}
	// Verify index.html exists
	if _, err := sub.Open("index.html"); err != nil {
		return nil
	}
	return sub
}
