# Search subsystem

Context Bridge supports one configured search mode per process. The default is
case-insensitive Go regular expressions; invalid patterns are
rejected with explicit error results and never fall back to a different
interpretation.

The optional FTS5 mode passes user input through `BuildLiteralFTS5Match`.
Whitespace-delimited terms are individually quoted as FTS5 string data, so raw
MATCH operators are not exposed. SQLite tokenization determines punctuation
behavior, and results use BM25 ordering.

Agent-facing MCP searches are bounded independently of the store's local UI
APIs:

- regex scans at most 250 recent capture candidates;
- either mode returns at most 50 capture groups and 1,000 matched lines;
- snippets cap line and aggregate sizes;
- the complete MCP payload caps at 128 KiB.

All modes ignore dashboard-deleted sessions and captures older than 30 days.
Descriptions, content, and snippets are untrusted historical data.
