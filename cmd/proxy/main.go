package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"antigravity-go-proxy/internal/api"
	"antigravity-go-proxy/internal/auth"
	"antigravity-go-proxy/internal/cloudcode"
)

func main() {
	listen := flag.String("listen", envOr("ANTIGRAVITY_PROXY_LISTEN", "127.0.0.1:8091"), "HTTP listen address")
	apiKey := flag.String("api-key", envOr("ANTIGRAVITY_PROXY_API_KEY", os.Getenv("API_KEY")), "required local API key")
	projectID := flag.String("project", os.Getenv("AGY_PROJECT_ID"), "optional managed Cloud Code project ID")
	upstreamTimeout := flag.Duration("upstream-timeout", 5*time.Minute, "Cloud Code request timeout")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	handler, err := api.New(api.Options{
		APIKey: *apiKey, ProjectID: *projectID,
		Credentials: func(ctx context.Context) (auth.Credentials, error) {
			return (auth.Manager{}).Get(ctx)
		},
		NewUpstream: func(accessToken string) api.Upstream {
			return cloudcode.New(cloudcode.Options{AccessToken: accessToken, Timeout: *upstreamTimeout})
		},
		Logger: logger,
	})
	if err != nil {
		logger.Error("invalid proxy configuration", "error", err)
		os.Exit(2)
	}

	httpServer := &http.Server{
		Addr: *listen, Handler: handler.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	shutdownSignals := make(chan os.Signal, 1)
	signal.Notify(shutdownSignals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-shutdownSignals
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
		}
	}()

	logger.Info("antigravity Go proxy listening", "address", *listen)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("HTTP server failed", "error", err)
		os.Exit(1)
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
