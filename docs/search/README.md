# Search implementation status

| Component | Status | Evidence |
| --- | --- | --- |
| Config mechanism | ✅ | `internal/config/config.go` accepts only `regex` or `fts5` |
| FTS5 sanitizer | ✅ | `internal/search/fts5.go` quotes every term as data |
| BM25 ranking | ✅ | `internal/store/store.go` orders FTS5 hits by hidden `rank` |
| Regex rejection | ✅ | Invalid Go regular expressions return an explicit error |
| Mode-aware RenderHint | ✅ | `RenderHint` emits guidance for the configured mode |
| MCP description alignment | ✅ | `internal/mcp/mcp.go` advertises mode-specific syntax and bounds |

The MCP surface is deliberately limited to `list`, `read`, and `search`.
Results are bounded and wrapped as untrusted historical data.

See [MCP tool contract](./mcp-tool-contract.md) for the request schema and
[vector search](./vector-search.md) for an explicitly unimplemented idea.
