package main

import (
	"io/fs"

	"github.com/batuta-ai/console/internal/webui"
)

func webFiles() fs.FS {
	sub, err := fs.Sub(webui.Dist, "dist")
	if err != nil {
		return webui.Dist
	}
	return sub
}
