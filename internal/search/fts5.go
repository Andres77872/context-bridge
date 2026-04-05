package search

import (
	"errors"
	"strings"
)

// ErrEmptyQuery indicates that the input query was empty or whitespace-only.
var ErrEmptyQuery = errors.New("query is required")

// BuildLiteralFTS5Match builds a safe FTS5 MATCH expression from user input.
// Each whitespace-delimited term becomes a quoted FTS5 string literal.
// Whitespace between quoted phrases creates implicit AND in FTS5 semantics.
//
// This function prevents FTS5 query injection by treating all input as literal
// terms rather than FTS5 operators (AND, OR, NOT, *, column:, NEAR, etc).
//
// Example: "auth token timeout" becomes `"auth" "token" "timeout"`
// which matches captures containing all three terms (implicit AND).
func BuildLiteralFTS5Match(input string) (string, error) {
	fields := strings.Fields(strings.TrimSpace(input))
	if len(fields) == 0 {
		return "", ErrEmptyQuery
	}
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		parts = append(parts, quoteFTS5String(f))
	}
	return strings.Join(parts, " "), nil
}

// quoteFTS5String wraps a term in FTS5 string literal syntax with proper escaping.
// FTS5 string literals use double quotes around the term, and embedded double-quotes
// are escaped by doubling them (" becomes "").
//
// Example: "hello"world" becomes `"hello""world"`
// Example: "auth" becomes `"auth"`
func quoteFTS5String(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
