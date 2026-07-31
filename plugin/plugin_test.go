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
