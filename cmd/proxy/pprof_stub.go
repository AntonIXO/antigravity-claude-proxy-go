//go:build !pprof

package main

import "log/slog"

func init() {
	startPprof = func(*slog.Logger) {}
}
