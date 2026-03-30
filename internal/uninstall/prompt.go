package uninstall

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

func Prompt(plan Plan, stdin io.Reader, stdout io.Writer) (Mode, bool, error) {
	reader := bufio.NewReader(stdin)

	if _, err := fmt.Fprint(stdout, RenderPlan(plan, "")); err != nil {
		return "", false, err
	}
	if _, err := fmt.Fprintln(stdout, ""); err != nil {
		return "", false, err
	}
	if _, err := fmt.Fprintln(stdout, "Select uninstall mode:"); err != nil {
		return "", false, err
	}
	if _, err := fmt.Fprintln(stdout, "  1) Full removal (default) — remove binaries, OpenCode integration, config, and data"); err != nil {
		return "", false, err
	}
	if _, err := fmt.Fprintln(stdout, "  2) Preserve data — remove binaries and OpenCode integration, keep config and data"); err != nil {
		return "", false, err
	}
	if _, err := fmt.Fprint(stdout, "Choice [1/2]: "); err != nil {
		return "", false, err
	}

	choice, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", false, err
	}

	mode := ModeFull
	switch strings.TrimSpace(strings.ToLower(choice)) {
	case "", "1", string(ModeFull):
		mode = ModeFull
	case "2", string(ModePreserveData):
		mode = ModePreserveData
	default:
		return "", false, fmt.Errorf("invalid selection %q", strings.TrimSpace(choice))
	}

	if _, err := fmt.Fprintf(stdout, "Type 'yes' to confirm %s uninstall: ", mode); err != nil {
		return "", false, err
	}
	confirm, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", false, err
	}

	return mode, strings.EqualFold(strings.TrimSpace(confirm), "yes"), nil
}
