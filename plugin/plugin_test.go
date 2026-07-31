package plugin

import (
	"strings"
	"testing"
)

func TestEmbeddedOpenCodeAdapterCarriesIsolationAndRuntimeRegistrationHooks(t *testing.T) {
	source := string(OpenCodePlugin)
	for _, required := range []string{
		"config: async (config)",
		`"tool.execute.before"`,
		"output.args.session_id = hookInput.sessionID",
		`output?.metadata?.background === true`,
		`health.service === BRIDGE_SERVICE`,
		`health.protocol === BRIDGE_PROTOCOL`,
		"AbortSignal.timeout",
		"canonicalAbsolutePath",
		"normalize(path)",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("embedded adapter is missing contract fragment %q", required)
		}
	}
	for _, forbidden := range []string{"opencode.json", "opencode.jsonc", "mem_search", "**REQUIRED**"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("embedded adapter contains forbidden coupling %q", forbidden)
		}
	}
}

// TestEmbeddedOpenCodeAdapterIsADualRuntimeModule pins the module shape both
// OpenCode plugin loaders read. The V1 loader requires a default export with
// id + server(); the V2 loader decodes a default export with id + setup(). One
// module satisfies both so the adapter keeps working while V2 grows the domains
// capture needs.
func TestEmbeddedOpenCodeAdapterIsADualRuntimeModule(t *testing.T) {
	source := string(OpenCodePlugin)
	for _, required := range []string{
		`import type { PluginContext as PluginContextV2 } from "@opencode-ai/plugin/v2/promise"`,
		`const setup = async (context: PluginContextV2)`,
		"export default {",
		`id: "context-bridge"`,
		"setup,",
		"server,",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("embedded adapter is missing dual-runtime fragment %q", required)
		}
	}
}

// TestEmbeddedOpenCodeAdapterKeepsOneCaptureOwner proves the two runtimes cannot
// both capture. V2 claims ownership only after its tool hook is installed, and
// every V1 capture path checks that flag first.
func TestEmbeddedOpenCodeAdapterKeepsOneCaptureOwner(t *testing.T) {
	source := string(OpenCodePlugin)

	if strings.Count(source, "v2OwnsCapture = true") != 1 {
		t.Fatal("V2 ownership must be claimed in exactly one place")
	}
	if !strings.Contains(source, "if (!(await registerV2Capture(domains))) return;") {
		t.Fatal("V2 must stay inert unless its capture hook registered")
	}
	for _, guarded := range []string{
		"event: async ({ event }) => {\n      if (v2OwnsCapture) return;",
		"\"tool.execute.after\": async (hookInput, output) => {\n      if (v2OwnsCapture) return;",
	} {
		if !strings.Contains(source, guarded) {
			t.Fatalf("V1 pipeline must stand down when V2 owns capture, missing %q", guarded)
		}
	}
}

// TestEmbeddedOpenCodeAdapterCarriesInstallerContract keeps the adapter
// patchable: the installer rewrites exactly one BRIDGE_BIN declaration.
func TestEmbeddedOpenCodeAdapterCarriesInstallerContract(t *testing.T) {
	marker := `const BRIDGE_BIN = process.env.CONTEXT_BRIDGE_BIN ?? Bun.which("context-bridge") ?? "context-bridge";`
	if count := strings.Count(string(OpenCodePlugin), marker); count != 1 {
		t.Fatalf("expected exactly one BRIDGE_BIN declaration, got %d", count)
	}
}
