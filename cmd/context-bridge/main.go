package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"context-bridge/internal/config"
	bridgeMCP "context-bridge/internal/mcp"
	"context-bridge/internal/opencode"
	"context-bridge/internal/server"
	"context-bridge/internal/store"
	"context-bridge/internal/tui"
	"context-bridge/internal/uninstall"
	"context-bridge/internal/update"
	"context-bridge/internal/web"
)

var version = "dev"

var (
	commandStdin   io.Reader = os.Stdin
	commandStdout  io.Writer = os.Stdout
	commandStderr  io.Writer = os.Stderr
	runUninstall             = uninstall.Run
	runUpdate                = update.Run
	dialUnixSocket           = net.DialTimeout
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
	case "stop":
		return cmdStop(args[1:])
	case "mcp":
		return cmdMCP(args[1:])
	case "tui":
		return cmdTUI(args[1:])
	case "web":
		return cmdWeb(args[1:])
	case "integration":
		return cmdIntegration(args[1:])
	case "update":
		return cmdUpdate(args[1:])
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
	addr := fs.String("addr", "", "Explicit loopback TCP listen address (Unix socket is the secure default)")
	socketPath := fs.String("socket", "", "Unix socket path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *addr == "" && *socketPath == "" {
		*addr = strings.TrimSpace(os.Getenv("CONTEXT_BRIDGE_ADDR"))
		*socketPath = strings.TrimSpace(os.Getenv("CONTEXT_BRIDGE_SOCKET"))
		if *addr == "" && *socketPath == "" && strings.TrimSpace(os.Getenv("CONTEXT_BRIDGE_PORT")) != "" {
			*addr = defaultHTTPAddr()
		}
		if *addr == "" && *socketPath == "" {
			resolvedSocketPath, err := config.ResolveSocketPath(os.LookupEnv, os.UserConfigDir, os.UserHomeDir)
			if err != nil {
				return fmt.Errorf("resolve Unix socket path: %w", err)
			}
			*socketPath = resolvedSocketPath
		}
	}
	if (*addr == "") == (*socketPath == "") {
		return errors.New("serve requires exactly one of --socket or --addr")
	}
	if *socketPath != "" {
		cleanSocket, err := validateSocketPath(*socketPath)
		if err != nil {
			return err
		}
		*socketPath = cleanSocket
	}
	if *addr != "" {
		if err := validateLoopbackAddr(*addr); err != nil {
			return err
		}
	}

	cfg, _, err := loadRuntimeConfig()
	if err != nil {
		return err
	}

	st, err := openStore()
	if err != nil {
		return err
	}

	shutdownRequested := make(chan struct{}, 1)
	var requestShutdown func()
	if *socketPath != "" {
		requestShutdown = func() {
			select {
			case shutdownRequested <- struct{}{}:
			default:
			}
		}
	}
	srv := server.NewWithVersionAndShutdown(st, store.SearchMode(cfg.SearchMode), version, requestShutdown)
	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	listener, cleanupListener, err := bridgeListener(*addr, *socketPath)
	if err != nil {
		_ = st.Close()
		return err
	}
	defer cleanupServeResources(st, cleanupListener)

	errCh := make(chan error, 1)
	go func() {
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case <-ctx.Done():
	case <-shutdownRequested:
	case err := <-errCh:
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

func cleanupServeResources(st io.Closer, cleanupListener func()) {
	_ = st.Close()
	cleanupListener()
}

func cmdStop(args []string) error {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	defaultSocketPath, err := config.ResolveSocketPath(os.LookupEnv, os.UserConfigDir, os.UserHomeDir)
	if err != nil {
		return fmt.Errorf("resolve Unix socket path: %w", err)
	}
	socketPath := fs.String("socket", defaultSocketPath, "Unix socket path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cleanSocket, err := validateSocketPath(*socketPath)
	if err != nil {
		return err
	}
	*socketPath = cleanSocket
	client := unixHTTPClient(*socketPath, 2*time.Second)
	request, err := http.NewRequest(http.MethodPost, "http://context-bridge/shutdown", strings.NewReader(`{}`))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("stop context-bridge at %s: %w", *socketPath, err)
	}
	defer response.Body.Close()
	var body struct {
		OK       bool `json:"ok"`
		Shutdown bool `json:"shutdown"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&body); err != nil {
		return fmt.Errorf("decode shutdown response: %w", err)
	}
	if response.StatusCode != http.StatusOK || !body.OK || !body.Shutdown {
		return fmt.Errorf("bridge refused shutdown with HTTP %d", response.StatusCode)
	}
	return nil
}

func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, _, err := loadRuntimeConfig()
	if err != nil {
		return err
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

	cfg, cfgPath, err := loadRuntimeConfig()
	if err != nil {
		return err
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
	open := fs.Bool("open", false, "Open browser automatically (token appears briefly in launcher arguments)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validateLoopbackAddr(*addr); err != nil {
		return err
	}

	cfg, cfgPath, err := loadRuntimeConfig()
	if err != nil {
		return err
	}

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	return web.Run(st, *addr, version, store.SearchMode(cfg.SearchMode), cfgPath, *open)
}

func openStore() (*store.Store, error) {
	location, err := config.ResolveDBLocation(os.LookupEnv, os.UserHomeDir)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	if location.ManagedDirectory {
		return store.OpenManaged(location.Path)
	}
	return store.Open(location.Path)
}

func loadRuntimeConfig() (config.Config, string, error) {
	cfgPath, err := config.ResolveConfigPath(os.LookupEnv, os.UserConfigDir, os.UserHomeDir)
	if err != nil {
		return config.Config{}, "", fmt.Errorf("resolve config path: %w", err)
	}
	explicit := strings.TrimSpace(os.Getenv("CONTEXT_BRIDGE_CONFIG")) != ""
	cfg, err := config.LoadConfig(cfgPath, explicit)
	if err != nil {
		return config.Config{}, "", fmt.Errorf("load config: %w", err)
	}
	return cfg, cfgPath, nil
}

func defaultHTTPAddr() string {
	port := envOrDefault("CONTEXT_BRIDGE_PORT", "7438")
	return "127.0.0.1:" + port
}

func validateLoopbackAddr(addr string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil || strings.TrimSpace(port) == "" {
		return fmt.Errorf("serve address must be a loopback host:port: %q", addr)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("refusing non-loopback serve address %q", addr)
	}
	return nil
}

func validateSocketPath(socketPath string) (string, error) {
	socketPath = strings.TrimSpace(socketPath)
	if socketPath == "" {
		return "", errors.New("Unix socket path is required")
	}
	if !filepath.IsAbs(socketPath) {
		return "", fmt.Errorf("Unix socket path must be absolute: %q", socketPath)
	}
	return filepath.Clean(socketPath), nil
}

func bridgeListener(addr, socketPath string) (net.Listener, func(), error) {
	if addr != "" {
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, func() {}, fmt.Errorf("listen on %s: %w", addr, err)
		}
		return listener, func() { _ = listener.Close() }, nil
	}

	absSocket, err := validateSocketPath(socketPath)
	if err != nil {
		return nil, func() {}, err
	}
	dir := filepath.Dir(absSocket)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, func() {}, fmt.Errorf("create Unix socket directory: %w", err)
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil || dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return nil, func() {}, fmt.Errorf("Unix socket directory must be a real directory: %s", dir)
	}
	if dirInfo.Mode().Perm()&0o077 != 0 {
		if filepath.Base(dir) != "context-bridge" {
			return nil, func() {}, fmt.Errorf("Unix socket directory must be private (0700): %s", dir)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, func() {}, fmt.Errorf("secure Unix socket directory: %w", err)
		}
	}

	if info, err := os.Lstat(absSocket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, func() {}, fmt.Errorf("refusing to replace non-socket path %s", absSocket)
		}
		conn, dialErr := dialUnixSocket("unix", absSocket, 200*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return nil, func() {}, fmt.Errorf("context-bridge socket is already in use: %s", absSocket)
		}
		if !errors.Is(dialErr, os.ErrNotExist) && !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return nil, func() {}, fmt.Errorf("could not verify existing Unix socket %s is stale; refusing to replace it: %w", absSocket, dialErr)
		}
		if errors.Is(dialErr, syscall.ECONNREFUSED) {
			current, inspectErr := os.Lstat(absSocket)
			switch {
			case errors.Is(inspectErr, os.ErrNotExist):
			case inspectErr != nil:
				return nil, func() {}, fmt.Errorf("reinspect stale Unix socket: %w", inspectErr)
			case !os.SameFile(info, current):
				return nil, func() {}, fmt.Errorf("Unix socket %s changed while checking whether it was stale", absSocket)
			default:
				if err := os.Remove(absSocket); err != nil && !errors.Is(err, os.ErrNotExist) {
					return nil, func() {}, fmt.Errorf("remove stale Unix socket: %w", err)
				}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, func() {}, fmt.Errorf("inspect Unix socket: %w", err)
	}

	listener, err := net.Listen("unix", absSocket)
	if err != nil {
		return nil, func() {}, fmt.Errorf("listen on Unix socket %s: %w", absSocket, err)
	}
	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		_ = listener.Close()
		_ = os.Remove(absSocket)
		return nil, func() {}, fmt.Errorf("Unix listener has unexpected type %T", listener)
	}
	unixListener.SetUnlinkOnClose(false)
	if err := os.Chmod(absSocket, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(absSocket)
		return nil, func() {}, fmt.Errorf("secure Unix socket: %w", err)
	}
	ownedInfo, err := os.Lstat(absSocket)
	if err != nil {
		_ = listener.Close()
		_ = os.Remove(absSocket)
		return nil, func() {}, fmt.Errorf("inspect created Unix socket: %w", err)
	}
	cleanup := func() {
		_ = listener.Close()
		if current, err := os.Lstat(absSocket); err == nil && os.SameFile(ownedInfo, current) {
			_ = os.Remove(absSocket)
		}
	}
	return listener, cleanup, nil
}

func unixHTTPClient(socketPath string, timeout time.Duration) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

func cmdUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	targetVersion := fs.String("version", "latest", "Release tag to install, e.g. v0.3.0 (default: latest)")
	check := fs.Bool("check", false, "Report whether a newer release is available without installing it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: context-bridge update [--version <tag>] [--check]")
	}
	return runUpdate(update.Options{
		TargetVersion:  *targetVersion,
		CheckOnly:      *check,
		CurrentVersion: version,
		Stdout:         commandStdout,
		Stderr:         commandStderr,
	})
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

func cmdIntegration(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: context-bridge integration install [--owned-binary]|status|uninstall")
	}

	switch args[0] {
	case "install":
		fs := flag.NewFlagSet("integration install", flag.ContinueOnError)
		ownedBinary := fs.Bool("owned-binary", false, "Record binary ownership (hosted installer only)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("usage: context-bridge integration install [--owned-binary]")
		}
		executable, err := currentExecutable()
		if err != nil {
			return err
		}
		var path string
		if *ownedBinary {
			path, err = opencode.InstallOwnedPlugin(executable)
		} else {
			path, err = opencode.InstallPlugin(executable)
		}
		if err != nil {
			return fmt.Errorf("install OpenCode integration: %w", err)
		}
		_, err = fmt.Fprintf(commandStdout, "OpenCode integration installed: %s\n", path)
		return err
	case "status":
		if len(args) != 1 {
			return errors.New("usage: context-bridge integration status")
		}
		status, err := opencode.InspectIntegration()
		if err != nil {
			return fmt.Errorf("inspect OpenCode integration: %w", err)
		}
		if _, err := fmt.Fprintf(commandStdout, "state: %s\nplugin: %s\nmanifest: %s\n", status.State, status.PluginPath, status.ManifestPath); err != nil {
			return err
		}
		if status.Reason != "" {
			_, err = fmt.Fprintf(commandStdout, "reason: %s\n", status.Reason)
		}
		return err
	case "uninstall":
		if len(args) != 1 {
			return errors.New("usage: context-bridge integration uninstall")
		}
		removed, path, err := opencode.RemovePlugin()
		if err != nil {
			return fmt.Errorf("uninstall OpenCode integration: %w", err)
		}
		if !removed {
			_, err = fmt.Fprintf(commandStdout, "OpenCode integration is not installed: %s\n", path)
			return err
		}
		_, err = fmt.Fprintf(commandStdout, "OpenCode integration removed: %s\n", path)
		return err
	default:
		return fmt.Errorf("unknown integration action %q: use install, status, or uninstall", args[0])
	}
}

func currentExecutable() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve current executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return "", fmt.Errorf("resolve absolute executable path: %w", err)
	}
	return filepath.Clean(executable), nil
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
  context-bridge serve    Start private Unix-socket server for the OpenCode adapter
                          Flags: --socket, or explicit loopback TCP --addr
  context-bridge stop     Gracefully stop the private Unix-socket server
  context-bridge mcp      Start MCP stdio server for agent-facing tools
  context-bridge tui      Start terminal browser
  context-bridge web      Start web dashboard (default: 127.0.0.1:7440)
                          Flags: --addr, --open (default: false)
  context-bridge integration install|status|uninstall
                          Manage the owned OpenCode adapter without editing OpenCode config
  context-bridge update   Download, verify, and install the latest release in place
                          Flags: --version <tag> (default: latest), --check
  context-bridge uninstall Remove project-owned binaries and integrations
  context-bridge version  Print version`)
}
