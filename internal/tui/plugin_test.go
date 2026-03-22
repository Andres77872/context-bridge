package tui

import (
	"bytes"
	"testing"
)

func TestPatchBridgeBINLine(t *testing.T) {
	src := []byte(`import { Server } from "mcp";
const BRIDGE_BIN = process.env.CONTEXT_BRIDGE_BIN ?? Bun.which("context-bridge") ?? "context-bridge";
console.log(BRIDGE_BIN);`)

	expected := `import { Server } from "mcp";
const BRIDGE_BIN = process.env.CONTEXT_BRIDGE_BIN ?? Bun.which("context-bridge") ?? "/absolute/path/to/context-bridge";
console.log(BRIDGE_BIN);`

	patched := patchBridgeBINLine(src, "/absolute/path/to/context-bridge")
	if string(patched) != expected {
		t.Fatalf("expected:\n%s\ngot:\n%s", expected, string(patched))
	}

	// Should not change if it's just "context-bridge"
	patchedNoop := patchBridgeBINLine(src, "context-bridge")
	if string(patchedNoop) != string(src) {
		t.Fatalf("expected no-op, got:\n%s", string(patchedNoop))
	}
}

func TestStripJSONC(t *testing.T) {
	input := []byte(`{
		// Single line comment
		"key": "value", /* inline comment */
		"nested": {
			"url": "http://example.com" // url with slashes
		}
	}`)

	expected := `{
		
		"key": "value", 
		"nested": {
			"url": "http://example.com" 
		}
	}`

	stripped := stripJSONC(input)
	if !bytes.Equal(stripped, []byte(expected)) {
		t.Fatalf("expected:\n%s\ngot:\n%s", expected, string(stripped))
	}
}
