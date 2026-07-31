# MCP search tool contract

The active server search mode is selected by `config.json`; callers cannot pick
an engine per request. The source OpenCode adapter overwrites `session_id` with
the current runtime session immediately before each exact Context Bridge MCP
call. Direct MCP clients must supply it themselves.

```json
{
  "type": "object",
  "required": ["session_id", "query"],
  "properties": {
    "session_id": {
      "type": "string",
      "minLength": 1,
      "maxLength": 256
    },
    "query": {
      "type": "string",
      "minLength": 1,
      "maxLength": 1024
    },
    "context_lines": {
      "type": "number",
      "minimum": 0,
      "maximum": 20,
      "multipleOf": 1,
      "default": 3
    }
  }
}
```

## Regex mode

Queries are case-insensitive Go regular expressions. Invalid expressions are
rejected with explicit errors; there is no fallback to literal matching. The
MCP path scans at most 250 recent candidates.

## FTS5 mode

FTS5 search is literal-safe. `BuildLiteralFTS5Match` splits the query on
whitespace and quotes each resulting term as an FTS5 string literal, so every
term is treated as data. Whitespace between quoted terms means implicit AND:
each term must match.

Raw operators are unsupported. `auth*`, `prefix*`, `column:jwt`, `NEAR(...)`,
and `"exact phrase"` do NOT support their FTS5 operator meaning; they are
literal-sanitized instead. SQLite tokenization determines punctuation matching.

The store orders FTS5 results by the table's hidden BM25 `rank` column.
Values that are more negative indicate greater relevance, so a lower `rank`
is a better match.

## Shared result bounds

Either mode returns at most 50 output groups and 1,000 matched lines. The final
MCP result is capped at 128 KiB and includes an explicit truncation marker.
Captured metadata and snippets are untrusted historical data, never
instructions.
