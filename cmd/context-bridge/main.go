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

	bridgeMCP "context-bridge/internal/mcp"
	"context-bridge/internal/migrate"
	"context-bridge/internal/server"
	"context-bridge/internal/store"
	"context-bridge/internal/tui"
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
	case "migrate":
		return cmdMigrate(args[1:])
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

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	return bridgeMCP.Serve(st, version)
}

func cmdTUI(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	return tui.Run(st)
}

func cmdMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	from := fs.String("from", defaultImportDir(), "legacy OpenCode sessions directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	count, err := migrate.ImportManifestDir(st, *from)
	if err != nil {
		return err
	}

	fmt.Printf("imported %d capture(s) from %s\n", count, *from)
	return nil
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

func defaultImportDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "sessions"
		}
		base = filepath.Join(home, ".local", "share")
	}

	return filepath.Join(base, "opencode", "tool-output", "sessions")
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
  context-bridge migrate  Import legacy manifest + markdown data
  context-bridge version  Print version`)
}
