package tui

import (
	"context-bridge/internal/opencode"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
)

type pluginInstalledMsg struct {
	err  error
	path string
}

func installPluginCmd() tea.Cmd {
	return func() tea.Msg {
		pluginPath, err := opencode.InstallPlugin(resolveContextBridgeCommand())
		if err != nil {
			return pluginInstalledMsg{err: fmt.Errorf("could not install plugin: %w", err)}
		}

		return pluginInstalledMsg{err: nil, path: filepath.Dir(pluginPath)}
	}
}

func resolveContextBridgeCommand() string {
	exe, err := os.Executable()
	if err != nil {
		return "context-bridge"
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe
}
