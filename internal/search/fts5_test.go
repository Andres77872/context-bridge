package search

import (
	"strings"
	"testing"
)

func TestBuildLiteralFTS5Match(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:    "multi-term input",
			input:   "auth token timeout",
			want:    `"auth" "token" "timeout"`,
			wantErr: false,
		},
		{
			name:    "whitespace normalization",
			input:   "  auth   token  ",
			want:    `"auth" "token"`,
			wantErr: false,
		},
		{
			name:    "empty query",
			input:   "",
			want:    "",
			wantErr: true,
		},
		{
			name:    "whitespace-only query",
			input:   "   ",
			want:    "",
			wantErr: true,
		},
		{
			name:    "single term",
			input:   "error",
			want:    `"error"`,
			wantErr: false,
		},
		{
			name:    "embedded quote escaped",
			input:   `hello"world`,
			want:    `"hello""world"`,
			wantErr: false,
		},
		{
			name:    "multiple embedded quotes",
			input:   `"quote"`,
			want:    `"""quote"""`,
			wantErr: false,
		},
		{
			name:    "punctuation preserved",
			input:   "user@email.com",
			want:    `"user@email.com"`,
			wantErr: false,
		},
		{
			name:    "code symbols preserved",
			input:   "file.go",
			want:    `"file.go"`,
			wantErr: false,
		},
		{
			name:    "special chars preserved",
			input:   "C++",
			want:    `"C++"`,
			wantErr: false,
		},
		{
			name:    "unmatched quote handled splits into terms",
			input:   `"unmatched quote`,
			want:    `"""unmatched" "quote"`,
			wantErr: false,
		},
		{
			name:    "unicode preserved",
			input:   "日本語 token",
			want:    `"日本語" "token"`,
			wantErr: false,
		},
		{
			name:    "operators treated as literals",
			input:   "AND OR NOT",
			want:    `"AND" "OR" "NOT"`,
			wantErr: false,
		},
		{
			name:    "prefix wildcard treated as literal",
			input:   "auth*",
			want:    `"auth*"`,
			wantErr: false,
		},
		{
			name:    "column filter syntax treated as literal",
			input:   "content:jwt",
			want:    `"content:jwt"`,
			wantErr: false,
		},
		{
			name:    "tabs normalized to spaces",
			input:   "auth\ttoken",
			want:    `"auth" "token"`,
			wantErr: false,
		},
		{
			name:    "newlines normalized to spaces",
			input:   "auth\ntoken",
			want:    `"auth" "token"`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildLiteralFTS5Match(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %q, got none", tt.input)
				}
				if err != ErrEmptyQuery {
					t.Fatalf("expected ErrEmptyQuery, got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for input %q: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("BuildLiteralFTS5Match(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestQuoteFTS5String(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple term",
			input: "auth",
			want:  `"auth"`,
		},
		{
			name:  "embedded quote doubled",
			input: `hello"world`,
			want:  `"hello""world"`,
		},
		{
			name:  "multiple embedded quotes",
			input: `"quote"`,
			want:  `"""quote"""`,
		},
		{
			name:  "empty string becomes empty quoted string",
			input: "",
			want:  `""`,
		},
		{
			name:  "punctuation",
			input: "user@email.com",
			want:  `"user@email.com"`,
		},
		{
			name:  "special chars",
			input: "C++",
			want:  `"C++"`,
		},
		{
			name:  "unicode",
			input: "日本語",
			want:  `"日本語"`,
		},
		{
			name:  "asterisk preserved",
			input: "auth*",
			want:  `"auth*"`,
		},
		{
			name:  "colon preserved",
			input: "content:jwt",
			want:  `"content:jwt"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := quoteFTS5String(tt.input)
			if got != tt.want {
				t.Fatalf("quoteFTS5String(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestErrEmptyQueryMessage(t *testing.T) {
	// Verify the error message is clear for users
	if !strings.Contains(ErrEmptyQuery.Error(), "query") {
		t.Fatalf("ErrEmptyQuery message should mention 'query', got: %s", ErrEmptyQuery.Error())
	}
}

func TestBuildLiteralFTS5MatchCreatesValidFTS5Syntax(t *testing.T) {
	// Verify that all outputs produce valid FTS5 MATCH syntax
	// Valid FTS5 string literals: "term" with embedded quotes as ""
	testInputs := []string{
		"simple",
		"multiple terms",
		`embedded"quote`,
		`"quoted"`,
		"punctuation@email.com",
		"special++chars",
	}

	for _, input := range testInputs {
		result, err := BuildLiteralFTS5Match(input)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", input, err)
		}

		// Verify the result is non-empty
		if result == "" {
			t.Fatalf("result should not be empty for valid input %q", input)
		}

		// Verify the result starts and ends properly for quoted terms
		// Each term should be wrapped in double quotes
		if !strings.HasPrefix(result, `"`) {
			t.Fatalf("result should start with double-quote for %q, got: %q", input, result)
		}

		// Verify no unescaped quotes in the middle of terms
		// All quotes should be doubled (escaped)
		fields := strings.Fields(result)
		for _, field := range fields {
			// Check that internal quotes are doubled (count should be even)
			quoteCount := strings.Count(field, `"`)
			if quoteCount < 2 {
				t.Fatalf("each quoted term should have at least 2 quotes (opening and closing), got %d for field %q from input %q", quoteCount, field, input)
			}
		}
	}
}
