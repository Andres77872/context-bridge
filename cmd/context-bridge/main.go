package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"context-bridge/internal/config"
	bridgeMCP "context-bridge/internal/mcp"
	"context-bridge/internal/server"
	"context-bridge/internal/store"
	"context-bridge/internal/tui"
	"context-bridge/internal/uninstall"
	"context-bridge/internal/web"
)

var version = "dev"

var (
	commandStdin  io.Reader = os.Stdin
	commandStdout io.Writer = os.Stdout
	commandStderr io.Writer = os.Stderr
	runUninstall            = uninstall.Run
)

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
	case "uninstall":
		return cmdUninstall(args[1:])
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
	open := fs.Bool("open", true, "Open browser automatically")
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

	return web.Run(st, *addr, version, store.SearchMode(cfg.SearchMode), cfgPath, *open)
}

func openStore() (*store.Store, error) {
	return store.Open(config.ResolveDBPath(os.LookupEnv, os.UserHomeDir))
}

func defaultHTTPAddr() string {
	port := envOrDefault("CONTEXT_BRIDGE_PORT", "7438")
	return "127.0.0.1:" + port
}

func cmdUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "Print the uninstall plan without removing artifacts")
	mode := fs.String("mode", "", "Removal mode for non-interactive uninstall: full or preserve-data")
	yes := fs.Bool("yes", false, "Execute uninstall non-interactively; requires --mode")
	if err := fs.Parse(args); err != nil {
		return err
	}

	return runUninstall(uninstall.Options{
		Mode:        uninstall.Mode(*mode),
		DryRun:      *dryRun,
		AssumeYes:   *yes,
		ExcludePath: os.Getenv("CONTEXT_BRIDGE_UNINSTALL_EXCLUDE_PATH"),
		Stdin:       commandStdin,
		Stdout:      commandStdout,
		Stderr:      commandStderr,
	})
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
                          Flags: --addr, --open (default: true)
  context-bridge uninstall Remove project-owned binaries and integrations
  context-bridge version  Print version`)
}
