# MCP server

`lore mcp` runs a [Model Context Protocol](https://modelcontextprotocol.io)
server, so any MCP client — Claude Desktop, editors, agent frameworks — can call
your private corpora as tools and get back **grounded, cited** answers (lore's
differentiator). It is also lore's first long-running process: it opens storage
and the vector index **once** at startup and reuses them across calls, so there's
no per-command cold start. It uses the same configuration as every other command
(provider, rerank, storage — flags > `LORE_*` > config file > defaults). See
[configuration](configuration.md) for the settings.

```bash
lore mcp                              # serve over stdio (for clients that spawn the binary)
lore mcp --collections docs,runbooks  # expose only these collections (names or globs)
lore mcp --http :8080                 # serve Streamable HTTP on 127.0.0.1:8080 instead
```

## Tools (read-only)

| Tool | What it does |
|---|---|
| `ask` | Answer a question using **only** the named collection(s); returns a grounded answer with citations to the exact passages (each citation carries the chunk text by default, so claims are verifiable without a second call). Honors `k`, `budget`, `rerank`, `source_glob`, `strict`, multi-collection merge. |
| `query` | Raw retrieval, no synthesis: the ranked chunks (text, source, score, chunk_id, collection) for an agent that wants to process the evidence itself. |
| `get_chunks` | Fetch specific chunks by ID — the exact passages an `ask` answer cited. Unknown IDs are omitted, not an error. |
| `list_collections` | Discover the exposed collections with their document count, model, dimensions, and chunker. |
| `collection_status` | Detailed status for one collection, including its chunk count. |

The tools map directly onto the `ask` / `query` / `cat --chunk` / `ls` / `status`
commands and the same `--rerank`, `--budget`, `--strict`, `--source`, and
multi-collection semantics — the MCP server wires the existing use cases to a
protocol surface, it does not reimplement them. lore's failures (unknown
collection, embedding-space mismatch across collections, `rerank` requested with
no rerank provider configured, `strict` with nothing to ground on) come back as
**MCP tool errors** — the server stays up across them.

## Claude Desktop

Register lore in `claude_desktop_config.json` (Settings → Developer → Edit
Config). Point `command` at the `lore` binary, pass `mcp` as the arg, and put
your provider/rerank/storage settings in `env`; scope what the agent can see with
`--collections`:

```json
{
  "mcpServers": {
    "lore": {
      "command": "lore",
      "args": ["mcp", "--collections", "docs,runbooks"],
      "env": {
        "LORE_DB_PATH": "/home/you/.config/lore/lore.db",
        "LORE_BASE_URL": "https://api.openai.com/v1",
        "LORE_API_KEY": "sk-…",
        "LORE_RERANK_BASE_URL": "https://api.cohere.ai/v1",
        "LORE_RERANK_API_KEY": "…",
        "LORE_RERANK_MODEL": "rerank-english-v3.0"
      }
    }
  }
}
```

The natural workflow ties to [portable corpora](corpora.md): build a collection,
`lore export` it (optionally encrypted), hand it off, `lore import` it on the
agent's host, and point `lore mcp` at it — a pre-indexed knowledge artifact an
agent can query and cite, no re-indexing required.

## Security posture

- **Read-only by default.** The server registers no `add`/`sync`/`rm`/`init`/
  `export` tools — an agent can query and verify, never mutate or delete a corpus.
- **Collection scoping.** `--collections <names-or-globs>` restricts the exposed
  set; an out-of-scope collection is absent from `list_collections` and a tool
  error everywhere else. Default is all local collections.
- **HTTP is local by default.** `--http :PORT` binds `127.0.0.1`. To bind a
  non-loopback address you **must** set a bearer token (`--http-token` or
  `LORE_MCP_TOKEN`), which every request must then carry as
  `Authorization: Bearer <token>`; lore refuses to start a network-reachable
  server without one. (OAuth is out of scope.)
- **Prompt-injection trust boundary.** Tool results are document chunks that flow
  into the calling model's context and may contain injected instructions. lore
  cannot sanitize this and does not interpret chunk text as instructions — but
  treating tool output as untrusted is the **client/operator's** responsibility,
  not lore's. Scope the server (`--collections`) to what an agent actually needs.
