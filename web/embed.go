package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

func FS() fs.FS { f, _ := fs.Sub(files, "static"); return f }
