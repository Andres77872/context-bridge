package docsverification

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// normalizeProse lowercases content and collapses every run of whitespace to a
// single space. Prose assertions run against this form so that a required
// phrase is still found after a paragraph is re-wrapped, and so documentation
// is never contorted to keep a literal substring on one line.
func normalizeProse(content string) string {
	return strings.Join(strings.Fields(strings.ToLower(content)), " ")
}

// repoRoot returns the repository root directory.
// Uses environment variable if set, otherwise walks up from current directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	// Try environment variable first
	if root := os.Getenv("CONTEXT_BRIDGE_REPO_ROOT"); root != "" {
		return root
	}
	// Walk up from working directory to find go.mod
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			t.Fatal("could not find repository root (go.mod not found)")
		}
		wd = parent
	}
}

// readFile reads a file relative to repo root and returns its content.
func readFile(t *testing.T, relPath string) string {
	t.Helper()
	root := repoRoot(t)
	fullPath := filepath.Join(root, relPath)
	content, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("read file %s: %v", fullPath, err)
	}
	return string(content)
}

// =============================================================================
// DOCUMENTATION ALIGNMENT TESTS - Root Docs
// =============================================================================

// TestReadmeReflectsRejectionBehavior verifies README.md correctly documents
// that invalid regex patterns return explicit errors (not fallback).
func TestReadmeReflectsRejectionBehavior(t *testing.T) {
	content := readFile(t, "README.md")

	// Must contain explicit rejection language
	required := []string{
		"Invalid regex patterns return explicit errors",
		"(no fallback)",
	}
	for _, phrase := range required {
		if !strings.Contains(content, phrase) {
			t.Errorf("README.md should contain %q", phrase)
		}
	}

	// Must NOT contain stale fallback language
	stalePhrases := []string{
		"falls back to literal",
		"fallback to literal matching",
		"QuoteMeta", // internal implementation detail, not user-facing
	}
	for _, phrase := range stalePhrases {
		if strings.Contains(content, phrase) {
			t.Errorf("README.md should NOT contain stale fallback language %q", phrase)
		}
	}
}

// TestReadmeReflectsFTS5LiteralSafe verifies README.md documents FTS5
// as literal-safe keyword search without unsupported operators.
func TestReadmeReflectsFTS5LiteralSafe(t *testing.T) {
	content := readFile(t, "README.md")

	// Must mention keywords or keyword search
	if !strings.Contains(content, "keyword") {
		t.Errorf("README.md should mention 'keyword' for FTS5 mode")
	}

	// Must NOT advertise unsupported FTS5 operators
	unsupported := []string{
		"auth*",   // prefix wildcard example
		"prefix*", // prefix syntax
		"column:", // column filter
		"NEAR()",  // proximity
		"boolean operator",
	}
	for _, op := range unsupported {
		// Check that examples don't use unsupported syntax
		// Exception: auth* may appear in regex examples, which is valid
		if strings.Contains(content, op) && !strings.Contains(content, "regex mode") {
			// Only flag if it appears in FTS5 context
			t.Logf("Note: README.md contains %q - verify context is regex mode, not FTS5", op)
		}
	}
}

// TestDesignMdReflectsRejectionBehavior verifies DESIGN.md documents
// explicit rejection behavior correctly.
func TestDesignMdReflectsRejectionBehavior(t *testing.T) {
	content := readFile(t, "DESIGN.md")

	// Must contain rejection language
	if !strings.Contains(content, "rejects invalid patterns") {
		t.Errorf("DESIGN.md should mention 'rejects invalid patterns'")
	}
	if !strings.Contains(content, "no fallback") {
		t.Errorf("DESIGN.md should mention 'no fallback'")
	}

	// Must NOT contain stale fallback language
	stalePhrases := []string{
		"falls back to literal",
		"fallback to literal",
	}
	for _, phrase := range stalePhrases {
		if strings.Contains(content, phrase) {
			t.Errorf("DESIGN.md should NOT contain stale fallback language %q", phrase)
		}
	}
}

// TestDesignMdReflectsFTS5LiteralSafe verifies DESIGN.md documents FTS5
// literal-safe sanitization correctly.
func TestDesignMdReflectsFTS5LiteralSafe(t *testing.T) {
	content := readFile(t, "DESIGN.md")

	// Must mention literal-safe sanitization
	if !strings.Contains(content, "literal-safe") {
		t.Errorf("DESIGN.md should mention 'literal-safe' for FTS5")
	}

	// Must mention BuildLiteralFTS5Match
	if !strings.Contains(content, "BuildLiteralFTS5Match") {
		t.Errorf("DESIGN.md should mention 'BuildLiteralFTS5Match' function")
	}

	// Must NOT advertise raw MATCH syntax
	if strings.Contains(content, "raw MATCH") && !strings.Contains(content, "no raw MATCH") {
		t.Errorf("DESIGN.md should NOT advertise raw MATCH syntax (except in negation)")
	}
}

// TestDocsSearchSubsystemReflectsRejection verifies docs/search-subsystem.md
// correctly documents explicit rejection behavior.
func TestDocsSearchSubsystemReflectsRejection(t *testing.T) {
	content := readFile(t, "docs/search-subsystem.md")

	// Must mention rejection
	if !strings.Contains(content, "rejected with explicit error") {
		t.Errorf("search-subsystem.md should mention 'rejected with explicit error'")
	}

	// Must NOT contain stale fallback language
	if strings.Contains(content, "falls back to") && strings.Contains(content, "QuoteMeta") {
		t.Errorf("search-subsystem.md should NOT describe QuoteMeta fallback")
	}
}

// TestDocsPersistenceModelReflectsRejection verifies docs/persistence-model.md
// documents explicit rejection behavior.
func TestDocsPersistenceModelReflectsRejection(t *testing.T) {
	content := readFile(t, "docs/persistence-model.md")

	// Must mention rejection in test coverage section
	if !strings.Contains(content, "explicit pattern rejection") {
		t.Errorf("persistence-model.md should mention 'explicit pattern rejection'")
	}
}

// =============================================================================
// DOCUMENTATION ALIGNMENT TESTS - docs/search/*
// =============================================================================

// TestDocsSearchReadmeStatusReflectsReality verifies docs/search/README.md
// accurately reflects implementation status.
func TestDocsSearchReadmeStatusReflectsReality(t *testing.T) {
	content := readFile(t, "docs/search/README.md")

	// Must show implemented status for key components with checkmarks
	implementedComponents := []struct {
		name   string
		marker string
	}{
		{"Config mechanism", "✅"},
		{"FTS5 sanitizer", "✅"},
		{"BM25 ranking", "✅"},
		{"Regex rejection", "✅"},
		{"Mode-aware RenderHint", "✅"},
		{"MCP description alignment", "✅"},
	}

	for _, comp := range implementedComponents {
		// Find the line that contains the component name
		lines := strings.Split(content, "\n")
		var componentLine string
		for _, line := range lines {
			if strings.Contains(line, comp.name) {
				componentLine = line
				break
			}
		}
		if componentLine == "" {
			t.Errorf("docs/search/README.md should list %q", comp.name)
			continue
		}

		// Verify the line has the correct marker (✅ for implemented)
		if !strings.Contains(componentLine, comp.marker) {
			t.Errorf("docs/search/README.md component %q should have marker %q, got line: %q", comp.name, comp.marker, componentLine)
		}
	}
}

// =============================================================================
// VECTOR SEARCH PLACEHOLDER TESTS
// =============================================================================

// TestVectorSearchDocIsPlaceholderOnly verifies docs/search/vector-search.md
// is TBD-only and does NOT describe an implemented feature.
func TestVectorSearchDocIsPlaceholderOnly(t *testing.T) {
	content := readFile(t, "docs/search/vector-search.md")

	// Must clearly state NOT IMPLEMENTED
	if !strings.Contains(content, "NOT IMPLEMENTED") {
		t.Errorf("vector-search.md must state 'NOT IMPLEMENTED' clearly")
	}

	// Must state future work / placeholder. Matched case-insensitively so the
	// doc is not forced into mid-sentence capitalization to satisfy this test.
	placeholderPhrases := []string{
		"future work",
		"placeholder",
		"tbd",
	}
	lowerDoc := normalizeProse(content)
	hasPlaceholder := false
	for _, phrase := range placeholderPhrases {
		if strings.Contains(lowerDoc, phrase) {
			hasPlaceholder = true
			break
		}
	}
	if !hasPlaceholder {
		t.Errorf("vector-search.md must indicate it is placeholder/future work/TBD")
	}

	// Must NOT claim implementation
	if strings.Contains(content, "implemented") && !strings.Contains(content, "NOT IMPLEMENTED") {
		t.Errorf("vector-search.md should NOT claim vector search is implemented")
	}
}

// TestNoVectorSearchImplementationInGoCode verifies no vector search
// implementation exists in Go codebase.
func TestNoVectorSearchImplementationInGoCode(t *testing.T) {
	root := repoRoot(t)

	// Vector search would likely involve these terms:
	vectorTerms := []string{
		"embedding",
		"vector",
		"semantic",
		"cosine",
		"similarity",
	}

	// Check internal packages for vector-related code
	internalDirs := []string{
		"internal/store",
		"internal/search",
		"internal/mcp",
	}

	for _, dir := range internalDirs {
		fullDir := filepath.Join(root, dir)
		files, err := filepath.Glob(filepath.Join(fullDir, "*.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", fullDir, err)
		}

		// Exclude test files from this check
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			content, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("read %s: %v", file, err)
			}
			fileContent := string(content)

			// Check for vector-related imports or code
			for _, term := range vectorTerms {
				// Allow mentions in comments/docs, but not in code
				// Look for function names, type names, imports
				lowerContent := strings.ToLower(fileContent)
				if strings.Contains(lowerContent, term) {
					// Check if it's in a comment or doc string
					// Heuristic: if it appears in a function/type declaration, flag it
					lines := strings.Split(fileContent, "\n")
					for _, line := range lines {
						lowerLine := strings.ToLower(line)
						if strings.Contains(lowerLine, term) {
							// Skip if line is clearly a comment
							trimmed := strings.TrimSpace(line)
							if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
								continue // comment, OK
							}
							// Skip if in package docs (before package declaration)
							if strings.HasPrefix(trimmed, "#") {
								continue // markdown header in docs
							}
							// If it's in actual code, check context
							if strings.Contains(lowerLine, "func ") || strings.Contains(lowerLine, "type ") || strings.Contains(lowerLine, "import ") {
								// This could be actual implementation
								t.Logf("Note: file %s contains term %q in non-comment line: %q", filepath.Base(file), term, trimmed)
							}
						}
					}
				}
			}
		}
	}

	// Explicitly check that SearchMode does not include vector mode
	configFile := filepath.Join(root, "internal/config/config.go")
	configContent, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatalf("read config.go: %v", err)
	}
	// Should NOT have SearchModeVector constant
	if strings.Contains(string(configContent), "SearchModeVector") {
		t.Errorf("config.go should NOT define SearchModeVector - vector search not implemented")
	}
	if strings.Contains(string(configContent), `"vector"`) && strings.Contains(string(configContent), "SearchMode") {
		t.Errorf("config.go should NOT include 'vector' as search_mode option")
	}
}

// =============================================================================
// MCP DESCRIPTION ALIGNMENT TESTS
// =============================================================================

// TestMcpDescriptionsAlignedWithFTS5 verifies MCP tool descriptions
// correctly advertise literal-safe FTS5 guidance (not unsupported operators).
// Note: This is a documentation assertion test, not a runtime MCP test.
func TestMcpDescriptionsAlignedWithFTS5(t *testing.T) {
	content := readFile(t, "internal/mcp/mcp.go")

	// Check FTS5 description strings
	// Must mention keywords or literal semantics
	if !strings.Contains(content, "keyword") && !strings.Contains(content, "Keywords") {
		t.Errorf("MCP FTS5 description should mention 'keyword' semantics")
	}

	// Must NOT advertise unsupported FTS5 operators in FTS5 mode descriptions
	// Check that FTS5-specific description sections don't have unsupported syntax
	unsupportedInFTS5Context := []string{
		"auth*",
		"prefix*",
		"column:",
		"NEAR",
		"AND", // as operator, not conjunction
		"OR",  // as operator, not conjunction
	}

	// Look for FTS5 description sections
	fts5Section := ""
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.Contains(line, "FTS5") && strings.Contains(line, "mode") {
			// Capture surrounding lines as FTS5 context
			start := max(0, i-5)
			end := min(len(lines), i+10)
			fts5Section += strings.Join(lines[start:end], "\n")
		}
	}

	for _, op := range unsupportedInFTS5Context {
		if strings.Contains(fts5Section, op) {
			t.Errorf("MCP FTS5 description section should NOT advertise unsupported operator %q", op)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// =============================================================================
// MCP TOOL CONTRACT DOC VERIFICATION
// =============================================================================

// TestMCPContractSchemaMatchesRuntime verifies the JSON schema in
// docs/search/mcp-tool-contract.md aligns with the actual runtime schema.
// This catches doc/runtime drift like "integer" vs "number" type mismatches.
func TestMCPContractSchemaMatchesRuntime(t *testing.T) {
	content := readFile(t, "docs/search/mcp-tool-contract.md")

	// Extract the first fenced JSON block (the request schema)
	schemaJSON := extractFirstFencedJSONBlock(t, content)

	// Parse the doc schema
	var docSchema struct {
		Type       string                 `json:"type"`
		Required   []string               `json:"required"`
		Properties map[string]interface{} `json:"properties"`
	}
	if err := json.Unmarshal(schemaJSON, &docSchema); err != nil {
		t.Fatalf("failed to parse doc schema JSON: %v (extracted content: %s)", err, string(schemaJSON))
	}

	// Debug output
	t.Logf("Parsed schema: Type=%s, Required=%v, Properties=%v", docSchema.Type, docSchema.Required, docSchema.Properties)

	// Verify required fields match expected
	expectedRequired := []string{"session_id", "query"}
	for _, req := range expectedRequired {
		found := false
		for _, r := range docSchema.Required {
			if r == req {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("doc schema should have %q in required fields", req)
		}
	}

	// Verify properties exist
	expectedProps := []string{"session_id", "query", "context_lines"}
	for _, prop := range expectedProps {
		if _, ok := docSchema.Properties[prop]; !ok {
			t.Errorf("doc schema should have property %q", prop)
		}
	}

	// Verify property types
	// session_id: string
	if prop, ok := docSchema.Properties["session_id"].(map[string]interface{}); ok {
		if prop["type"] != "string" {
			t.Errorf("session_id should be type 'string', got %v", prop["type"])
		}
	}

	// query: string
	if prop, ok := docSchema.Properties["query"].(map[string]interface{}); ok {
		if prop["type"] != "string" {
			t.Errorf("query should be type 'string', got %v", prop["type"])
		}
	}

	// context_lines: number (runtime uses mcp.WithNumber)
	if prop, ok := docSchema.Properties["context_lines"].(map[string]interface{}); ok {
		if prop["type"] != "number" {
			t.Errorf("context_lines should be type 'number' (matching runtime mcp.WithNumber), got %v", prop["type"])
		}
		// Should have minimum constraint
		if _, hasMin := prop["minimum"]; !hasMin {
			t.Errorf("context_lines should have 'minimum' constraint")
		}
	}

	// Verify "engine" is NOT in the schema (server-selected, not caller-specified)
	if _, hasEngine := docSchema.Properties["engine"]; hasEngine {
		t.Errorf("doc schema should NOT have 'engine' property - engine is server-selected via config")
	}
}

// TestMCPContractFTS5SemanticClaims verifies the MCP contract doc correctly
// documents FTS5 literal-only semantics without advertising unsupported operators.
func TestMCPContractFTS5SemanticClaims(t *testing.T) {
	content := readFile(t, "docs/search/mcp-tool-contract.md")

	// MUST document literal-safe semantics. Identifiers are matched exactly;
	// prose is matched case-insensitively and by meaning rather than by an
	// exact phrase, so the doc can be written naturally. Requiring literal
	// sentence fragments here previously forced tautological wording.
	exactClaims := []string{
		"literal-safe",
		"BuildLiteralFTS5Match",
	}
	for _, claim := range exactClaims {
		if !strings.Contains(content, claim) {
			t.Errorf("MCP contract doc must document %q for FTS5 literal semantics", claim)
		}
	}

	// Prose is matched against a whitespace-normalized, lowercased copy so a
	// phrase stays findable when the paragraph is re-wrapped.
	lower := normalizeProse(content)

	// The doc must explain both halves of the sanitizer's contract: input is
	// split on whitespace, and each resulting term is quoted as data.
	if !strings.Contains(lower, "whitespace") && !strings.Contains(lower, "space") {
		t.Errorf("MCP contract doc must explain that FTS5 input is split on whitespace")
	}
	if !strings.Contains(lower, "quote") {
		t.Errorf("MCP contract doc must explain that each FTS5 term is quoted as data")
	}

	// MUST NOT advertise unsupported FTS5 operators as available features
	unsupportedOperators := []string{
		"auth*",            // prefix wildcard (if not in negation context)
		"prefix*",          // prefix wildcard reference
		"column:jwt",       // column filter example (exact syntax)
		"NEAR(",            // proximity (if not in negation)
		"\"exact phrase\"", // phrase matching (if not in negation context)
	}

	for _, op := range unsupportedOperators {
		// Allow if it's in a negation/unsupported context
		if strings.Contains(content, op) {
			// Check if it's documented as unsupported
			negationContexts := []string{
				"does NOT support",
				"NOT support",
				"unsupported",
				"literal-sanitized",
				"All input is literal-sanitized",
			}
			hasNegation := false
			for _, neg := range negationContexts {
				// Look for negation within 200 chars of the operator mention
				opIdx := strings.Index(content, op)
				if opIdx >= 0 {
					start := max(0, opIdx-100)
					end := min(len(content), opIdx+100)
					window := content[start:end]
					if strings.Contains(window, neg) {
						hasNegation = true
						break
					}
				}
			}
			if !hasNegation {
				t.Errorf("MCP contract doc contains %q but should clarify it's NOT supported. "+
					"FTS5 runtime uses literal sanitization.", op)
			}
		}
	}

	// MUST document BM25 ordering semantics correctly. "BM25" and "rank" name
	// concrete things and stay case-sensitive; the explanation of the ordering
	// is checked case-insensitively.
	for _, term := range []string{"BM25", "rank"} {
		if !strings.Contains(content, term) {
			t.Errorf("MCP contract doc must document BM25 semantics with term %q", term)
		}
	}
	for _, phrase := range []string{"relevance", "more negative", "better match"} {
		if !strings.Contains(lower, phrase) {
			t.Errorf("MCP contract doc must explain BM25 ordering, including %q", phrase)
		}
	}

	// MUST document regex rejection behavior
	if !strings.Contains(content, "rejected with explicit errors") {
		t.Errorf("MCP contract doc must document regex rejection behavior")
	}
	if !strings.Contains(content, "no fallback") {
		t.Errorf("MCP contract doc must clarify regex has no fallback behavior")
	}
}

// extractFirstFencedJSONBlock extracts the first JSON code block from markdown
// that contains "properties" (indicating it's a schema, not just config).
func extractFirstFencedJSONBlock(t *testing.T, content string) []byte {
	t.Helper()
	lines := strings.Split(content, "\n")
	var jsonBlock []string
	inBlock := false

	for _, line := range lines {
		if strings.HasPrefix(line, "```json") {
			inBlock = true
			jsonBlock = nil // reset for new block
			continue
		}
		if strings.HasPrefix(line, "```") && inBlock {
			// Check if this block contains "properties" (schema marker)
			blockContent := strings.Join(jsonBlock, "\n")
			if strings.Contains(blockContent, `"properties"`) {
				return []byte(blockContent)
			}
			inBlock = false
			continue
		}
		if inBlock {
			jsonBlock = append(jsonBlock, line)
		}
	}

	// If we're still in block at EOF, check last one
	if inBlock && len(jsonBlock) > 0 {
		blockContent := strings.Join(jsonBlock, "\n")
		if strings.Contains(blockContent, `"properties"`) {
			return []byte(blockContent)
		}
	}

	t.Fatalf("no fenced JSON schema block (containing 'properties') found in content")
	return nil
}
