//go:build pprof

package main

import (
	"log/slog"
	"net/http"
	_ "net/http/pprof"
)

func init() {
	startPprof = func(logger *slog.Logger) {
		logger.Info("pprof server listening", "address", "localhost:6060")
		go func() {
			if err := http.ListenAndServe("localhost:6060", nil); err != nil {
				logger.Error("pprof server failed", "error", err)
			}
		}()
	}
}
