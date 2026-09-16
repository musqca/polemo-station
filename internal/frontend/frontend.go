package frontend

import "embed"

// Assets contains the static Station Web build. The Makefile's pkg target
// refreshes this directory before compiling the Station binary.
//
//go:embed all:build
var Assets embed.FS
