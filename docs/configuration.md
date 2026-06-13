# Configuration

Precedence: **flags > environment (`LORE_*`) > config file > defaults.** The
config file is TOML at `<user-config-dir>/lore/config.toml`.

| Env | TOML | Default | Meaning |
|---|---|---|---|
| `LORE_BASE_URL` | `provider.base_url` | `https://api.openai.com/v1` | OpenAI-compatible base URL |
| `LORE_API_KEY` | `provider.api_key` | — | API key |
| `LORE_AUTH` | `provider.auth` | `bearer` | `bearer` (OpenAI) or `api-key` (Azure) |
| `LORE_EMBED_MODEL` | `provider.embed_model` | `text-embedding-3-small` | embedding model / deployment |
| `LORE_DIMENSIONS` | `provider.dimensions` | `1536` | embedding dimensionality (must match the model) |
| `LORE_CHAT_MODEL` | `provider.chat_model` | `gpt-4o-mini` | chat model / deployment |
| `LORE_TIMEOUT` | `provider.timeout` | `120s` | per-request HTTP timeout (Go duration; `0` disables) |
| `LORE_EMBED_BASE_URL` | `provider.embed_base_url` | (shared `base_url`) | base URL for the **embed** role only |
| `LORE_EMBED_API_KEY` | `provider.embed_api_key` | (shared `api_key`) | API key for the embed role only |
| `LORE_EMBED_AUTH` | `provider.embed_auth` | (shared `auth`) | embed auth scheme: `bearer` or `api-key` |
| `LORE_EMBED_TIMEOUT` | `provider.embed_timeout` | (shared `timeout`) | embed per-request timeout (Go duration; `0` inherits shared) |
| `LORE_CHAT_BASE_URL` | `provider.chat_base_url` | (shared `base_url`) | base URL for the **chat** role only |
| `LORE_CHAT_API_KEY` | `provider.chat_api_key` | (shared `api_key`) | API key for the chat role only |
| `LORE_CHAT_AUTH` | `provider.chat_auth` | (shared `auth`) | chat auth scheme: `bearer` or `api-key` |
| `LORE_CHAT_TIMEOUT` | `provider.chat_timeout` | (shared `timeout`) | chat per-request timeout (Go duration; `0` inherits shared) |
| `LORE_STRUCTURED_OUTPUT` | `provider.structured_output` | `false` | request JSON-schema output (real citations) where supported |
| `LORE_IMAGE_INPUT` | `provider.image_input` | `false` | allow image attachments |
| `LORE_DOCUMENT_INPUT` | `provider.document_input` | `false` | allow document attachments |
| `LORE_RERANK_BASE_URL` | `rerank.base_url` | — | rerank endpoint base URL (Cohere-style) |
| `LORE_RERANK_API_KEY` | `rerank.api_key` | — | rerank API key |
| `LORE_RERANK_AUTH` | `rerank.auth` | `bearer` | rerank auth scheme: `bearer` or `api-key` |
| `LORE_RERANK_MODEL` | `rerank.model` | — | reranker model name |
| `LORE_RERANK_TIMEOUT` | `rerank.timeout` | `120s` | rerank per-request timeout (Go duration; `0` disables) |
| `LORE_STORAGE_BACKEND` | `storage.backend` | `sqlite` | `sqlite` or `memory` |
| `LORE_DB_PATH` | `storage.path` | `<user-config-dir>/lore/lore.db` | SQLite database file |
| `LORE_INGEST_CONCURRENCY` | `ingest.concurrency` | `8` | parallel embeds during ingest (lower for tight rate limits) |
| `LORE_CHUNK_STRATEGY` | `chunk.strategy` | `structure` | `structure` (heading/paragraph-aware) or `fixed` (word windows) |
| `LORE_CHUNK_SIZE` | `chunk.size` | `512` | target chunk size (tokens for `structure`, words for `fixed`) |
| `LORE_CHUNK_OVERLAP` | `chunk.overlap` | `64` | overlap between size-driven splits, same unit as size |
| `LORE_CHUNK_CONTEXT_PREFIX` | `chunk.context_prefix` | `true` | prepend a chunk's heading path to its embedded text (markdown) |
| `LORE_CACHE` | `cache.enabled` | `false` | reuse synthesized `ask`/`synthesize` answers across runs |
| `LORE_CACHE_TTL` | `cache.ttl` | `720h` (30d) | max age of a reusable cached answer (Go duration) |
| `LORE_LOG_LEVEL` | `log.level` | `info` | `debug`/`info`/`warn`/`error` |
| `LORE_LOG_FORMAT` | `log.format` | `text` | `text` or `json` |

Global flags override env, file, and defaults for the current command:

| Flag | Overrides | Meaning |
|---|---|---|
| `--config <path>` | — | read this TOML file instead of the default location |
| `--log-level <level>` | `log.level` | `debug`/`info`/`warn`/`error` |
| `--log-format <fmt>` | `log.format` | `text` or `json` |
| `-v`, `--verbose` | `log.level` | shorthand for `--log-level debug` |
| `--no-cache` | `cache.enabled` | bypass the answer cache for this run (`ask`/`synthesize`) |

The transient `429`/`503` responses of rate-limited providers are retried
automatically (honoring `Retry-After`).

## Answer cache

`ask` and `synthesize` can reuse a previously synthesized answer instead of
calling the model again — useful for repeated questions in scripts and CI. It is
**opt-in** (off by default); enable it once:

```bash
export LORE_CACHE=true          # or [cache] enabled = true in config.toml
lore ask notes "what is our key rotation policy?"   # first run: calls the model, caches
lore ask notes "what is our key rotation policy?"   # repeat: served from the cache, no model call
lore ask notes "…" --no-cache                        # force a fresh answer this run
```

The cache is keyed on the question, the exact text of the chunks that ground the
answer, and the model + prompt identity — so it **self-invalidates**: edit a
source document (changing its chunk text), switch models, or change `-k`/
`--source` enough to retrieve different chunks, and the next ask re-synthesizes.
Entries expire after `LORE_CACHE_TTL` (default 30 days). Requests with `--attach`
are never cached. The cache is stored in the SQLite database, so it persists
across invocations (it does nothing for the `memory` storage backend, which is
per-process).

## Azure OpenAI

Use Azure's v1 (OpenAI-compatible) surface and the `api-key` auth scheme; pass
your **deployment names** as the models:

```bash
export LORE_BASE_URL="https://<resource>.openai.azure.com/openai/v1"   # or *.cognitiveservices.azure.com
export LORE_AUTH=api-key
export LORE_API_KEY="<key>"
export LORE_EMBED_MODEL="<embedding-deployment>"   LORE_DIMENSIONS=3072
export LORE_CHAT_MODEL="<chat-deployment>"
```

## Split embed/chat endpoints

The bare `provider.*` connection (`base_url`/`api_key`/`auth`/`timeout`) is the
**shared default** for both roles. To target the embed and chat roles at
different services in one process, override either role independently with the
`embed_*` / `chat_*` connection settings; each field resolves **per-role override
→ shared `provider.*` → built-in default**. So you can embed against a local
model and generate against a hosted one in a single command:

```bash
# Embed locally (bearer), chat against Azure (api-key) — one process.
export LORE_EMBED_BASE_URL="http://localhost:8001/v1"
export LORE_EMBED_AUTH=bearer
export LORE_EMBED_MODEL="Qwen/Qwen3-Embedding-4B"   LORE_DIMENSIONS=2560
export LORE_BASE_URL="https://<resource>.openai.azure.com/openai/v1"
export LORE_AUTH=api-key   LORE_API_KEY="<azure-key>"
export LORE_CHAT_MODEL="<chat-deployment>"
lore ask kb "summarize the controls"
```

Or run fully local across three independent endpoints (rerank was already its
own provider — see above):

```bash
export LORE_EMBED_BASE_URL="http://localhost:8001/v1"  LORE_EMBED_MODEL="bge-m3"  LORE_DIMENSIONS=1024
export LORE_CHAT_BASE_URL="http://localhost:8003/v1"   LORE_CHAT_MODEL="qwen2.5-instruct"
export LORE_RERANK_BASE_URL="http://localhost:8002/v1" LORE_RERANK_MODEL="bge-reranker-v2-m3"
```

**Caveat — the embedding space is pinned to model + dimensions, not endpoint.**
A collection records the embedding model name and dimensionality; lore can't see
*which* service produced a vector. Pointing `embed_base_url` at a new endpoint
while keeping the same `embed_model` + `dimensions` will **not** trip the
space-mismatch guard — so if the new endpoint serves a genuinely different model
under the same name, the collection silently ends up with chunks embedded by two
different services in one nominal space, degrading retrieval. Repointing the
embed role is your assertion that the new endpoint serves the *same* embedding
model; if it doesn't, rebuild the collection (re-`init` + re-`add`).

## Attachments

Send a file straight to a capable model alongside (or instead of) retrieval —
useful for "compare this against the collection" or asking about an image:

```console
lore ask notes "compare this design to our standards" --attach proposal.pdf
lore ask notes "what's in this diagram?" -k 0 --attach architecture.png   # -k 0 = attachment only
```

Images encode as `image_url`, documents as a file part; each requires the
matching capability (`LORE_IMAGE_INPUT` / `LORE_DOCUMENT_INPUT`) to be enabled.
