package public

import "embed"

//go:embed all:dist
var Public embed.FS

//go:embed all:zb
var Zb embed.FS
