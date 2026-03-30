package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"context-bridge/internal/config"
	bridgeMCP "context-bridge/internal/mcp"
	"context-bridge/internal/server"
	"context-bridge/internal/store"
	"context-bridge/internal/tui"
	"context-bridge/internal/web"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "context-bridge: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}

	switch args[0] {
	case "serve":
		return cmdServe(args[1:])
	case "mcp":
		return cmdMCP(args[1:])
	case "tui":
		return cmdTUI(args[1:])
	case "web":
		return cmdWeb(args[1:])
	case "version":
		fmt.Println(version)
		return nil
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", envOrDefault("CONTEXT_BRIDGE_ADDR", defaultHTTPAddr()), "HTTP listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	srv := server.New(st)
	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfgPath := config.ResolveConfigPath(os.LookupEnv, os.UserConfigDir)
	cfg, err := config.LoadConfig(cfgPath, cfgPath != "" && os.Getenv("CONTEXT_BRIDGE_CONFIG") != "")
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	return bridgeMCP.Serve(st, version, bridgeMCP.SearchMode(cfg.SearchMode))
}

func cmdTUI(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfgPath := config.ResolveConfigPath(os.LookupEnv, os.UserConfigDir)
	cfg, err := config.LoadConfig(cfgPath, cfgPath != "" && os.Getenv("CONTEXT_BRIDGE_CONFIG") != "")
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	return tui.Run(st, store.SearchMode(cfg.SearchMode), cfgPath)
}
func cmdWeb(args []string) error {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	addr := fs.String("addr", envOrDefault("CONTEXT_BRIDGE_WEB_ADDR", "127.0.0.1:7440"), "Web dashboard listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfgPath := config.ResolveConfigPath(os.LookupEnv, os.UserConfigDir)
	cfg, err := config.LoadConfig(cfgPath, cfgPath != "" && os.Getenv("CONTEXT_BRIDGE_CONFIG") != "")
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	return web.Run(st, *addr, version, store.SearchMode(cfg.SearchMode), cfgPath)
}

func openStore() (*store.Store, error) {
	return store.Open(defaultDBPath())
}

func defaultDBPath() string {
	if value := os.Getenv("CONTEXT_BRIDGE_DB"); value != "" {
		return value
	}

	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "context-bridge.db"
		}
		base = filepath.Join(home, ".local", "share")
	}

	return filepath.Join(base, "context-bridge", "store.db")
}
func defaultHTTPAddr() string {
	port := envOrDefault("CONTEXT_BRIDGE_PORT", "7438")
	return "127.0.0.1:" + port
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func printUsage() {
	fmt.Println(`context-bridge

Usage:
  context-bridge serve    Start HTTP server for the thin OpenCode plugin
  context-bridge mcp      Start MCP stdio server for agent-facing tools
  context-bridge tui      Start read-only terminal browser
  context-bridge web      Start web dashboard (default: 127.0.0.1:7440)
  context-bridge version  Print version`)
}
