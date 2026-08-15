package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"antigravity-go-proxy/internal/accounts"
	"antigravity-go-proxy/internal/api"
	"antigravity-go-proxy/internal/auth"
	"antigravity-go-proxy/internal/cloudcode"
	"antigravity-go-proxy/internal/config"
	proxyformat "antigravity-go-proxy/internal/format"
	"antigravity-go-proxy/internal/logger"
	"antigravity-go-proxy/internal/stats"
	"antigravity-go-proxy/internal/webui"
	"path/filepath"
)

var startPprof func(*slog.Logger)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "stop":
			if err := StopDaemon(); err != nil {
				fmt.Fprintf(os.Stderr, "Error stopping proxy: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("✅ Antigravity proxy stopped.")
			return
		case "restart":
			_ = StopDaemon()
			time.Sleep(500 * time.Millisecond)
			pid, err := StartDaemon(os.Args[2:])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error restarting proxy daemon: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("✅ Antigravity proxy restarted (PID: %d)\n", pid)
			return
		case "status":
			listen := envOr("ANTIGRAVITY_PROXY_LISTEN", "127.0.0.1:8080")
			if err := cmdStatus(listen); err != nil {
				fmt.Fprintf(os.Stderr, "Error getting status: %v\n", err)
				os.Exit(1)
			}
			return
		case "web":
			listen := envOr("ANTIGRAVITY_PROXY_LISTEN", "127.0.0.1:8080")
			if err := cmdWeb(listen); err != nil {
				fmt.Fprintf(os.Stderr, "Error opening web UI: %v\n", err)
				os.Exit(1)
			}
			return
		case "accounts":
			if err := cmdAccounts(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		case "start":
			runServer(os.Args[2:])
			return
		}
	}

	// Default: run start command with current args
	runServer(os.Args[1:])
}

func runServer(args []string) {
	fs := flag.NewFlagSet("antigravity-proxy", flag.ExitOnError)
	listen := fs.String("listen", envOr("ANTIGRAVITY_PROXY_LISTEN", "127.0.0.1:8080"), "HTTP listen address")
	port := fs.Int("port", 0, "HTTP listen port (overrides port in -listen)")
	apiKey := fs.String("api-key", envOr("ANTIGRAVITY_PROXY_API_KEY", os.Getenv("API_KEY")), "optional local API key")
	projectID := fs.String("project", os.Getenv("AGY_PROJECT_ID"), "optional managed Cloud Code project ID")
	accountsPath := fs.String("accounts", os.Getenv("ANTIGRAVITY_ACCOUNTS_FILE"), "optional account-pool JSON path (default ~/.config/antigravity-proxy/accounts.json when present)")
	strategy := fs.String("strategy", envOr("ACCOUNT_STRATEGY", accounts.DefaultStrategy), "account strategy: sticky, round-robin, or hybrid")
	upstreamTimeout := fs.Duration("upstream-timeout", 5*time.Minute, "Cloud Code request timeout")
	pprof := fs.Bool("pprof", false, "enable pprof server on localhost:6060")
	daemon := fs.Bool("daemon", false, "run proxy server in background daemon mode")
	_ = fs.Parse(args)

	// Load configuration file
	cfg, _ := config.Load()
	if *apiKey == "" && cfg.APIKey != "" {
		*apiKey = cfg.APIKey
	}
	if *port > 0 {
		*listen = fmt.Sprintf("127.0.0.1:%d", *port)
	}

	// Daemon mode
	if *daemon {
		pid, err := StartDaemon(args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to start daemon: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✅ Antigravity proxy daemon started (PID: %d, Listen: %s)\n", pid, *listen)
		return
	}

	// Write PID file for foreground / daemon runner
	_ = WritePIDFile(os.Getpid())
	defer RemovePIDFile()

	// Broadcaster and logger
	broadcaster := logger.NewBroadcaster(500)
	logLevel := slog.LevelInfo
	if cfg.Debug || cfg.DevMode || os.Getenv("DEBUG") != "" || os.Getenv("ANTIGRAVITY_DEV_MODE") == "true" {
		logLevel = slog.LevelDebug
	}
	baseHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})
	streamHandler := logger.NewStreamHandler(baseHandler, broadcaster)
	slogger := slog.New(streamHandler)
	slog.SetDefault(slogger)

	// Usage statistics tracker
	statsPath := filepath.Join(config.GetConfigDir(), "usage-history.json")
	tracker, err := stats.NewTracker(statsPath)
	if err != nil {
		slogger.Warn("initialize stats tracker failed", "error", err)
	} else {
		tracker.StartAutoSave(1 * time.Minute)
		defer tracker.Close()
	}

	if *pprof && startPprof != nil {
		startPprof(slogger)
	}

	accountManager, err := accounts.NewDefault(*accountsPath, *strategy, nil)
	if err != nil {
		slogger.Error("load account pool", "error", err)
		os.Exit(2)
	}

	builder := proxyformat.NewBuilder()
	dispatcher, err := accounts.NewDispatcher(accounts.DispatcherOptions{
		Manager:   accountManager,
		Resolver:  accounts.NewCredentialResolver(auth.Manager{}, nil),
		Builder:   builder,
		ProjectID: *projectID,
		NewClient: func(accessToken string) accounts.CloudClient {
			return cloudcode.New(cloudcode.Options{AccessToken: accessToken, Timeout: *upstreamTimeout})
		},
	})
	if err != nil {
		slogger.Error("initialize account dispatcher", "error", err)
		os.Exit(2)
	}

	oauthMgr := auth.NewOAuthManager(accountManager)
	uiHandler := webui.Handler()

	handler, err := api.New(api.Options{
		APIKey:         *apiKey,
		Backend:        dispatcher,
		Builder:        builder,
		Logger:         slogger,
		AccountManager: accountManager,
		Broadcaster:    broadcaster,
		WebUI:          uiHandler,
		OAuthHandler:   oauthMgr,
		Tracker:        tracker,
	})
	if err != nil {
		slogger.Error("invalid proxy configuration", "error", err)
		os.Exit(2)
	}

	httpServer := &http.Server{
		Addr:              *listen,
		Handler:           handler.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	shutdownSignals := make(chan os.Signal, 1)
	signal.Notify(shutdownSignals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-shutdownSignals
		slogger.Info("shutting down proxy server...")
		if tracker != nil {
			_ = tracker.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			slogger.Error("graceful shutdown failed", "error", err)
		}
	}()

	slogger.Info("Antigravity proxy server listening", "address", *listen, "accounts", accountManager.Count(), "strategy", *strategy)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slogger.Error("HTTP server failed", "error", err)
		os.Exit(1)
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
