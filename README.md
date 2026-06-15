# lore — the auditable knowledge base

A fast, scriptable CLI for grounded, **cited**, and **verifiable** retrieval-augmented
generation (RAG) over *specific* sets of documents — built for pipes, scripts, and
CI, not GUIs. An answer is only as good as your ability to *prove* it's grounded, so
lore makes faithfulness verification and retrieval evaluation **CI-gateable** and
**machine-readable**.

[![CI](https://github.com/jmurray2011/lore/actions/workflows/ci.yml/badge.svg)](https://github.com/jmurray2011/lore/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/jmurray2011/lore)](https://github.com/jmurray2011/lore/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/jmurray2011/lore)](go.mod)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/jmurray2011/lore)](https://goreportcard.com/report/github.com/jmurray2011/lore)

```console
$ lore init notes
$ lore add notes ./docs
$ lore ask notes "how does auth work?"
```

<!-- TODO: record — demo placeholder; replace the link below with the cast/GIF.
     Record script (~25s; the grounded, cited answer is the payoff):
       lore init notes && lore add notes ./docs && lore ask notes "how does auth work?"
     e.g. `asciinema rec lore-demo.cast -c 'bash demo.sh'`, then `asciinema upload`
     and point the link at the result — or `agg lore-demo.cast docs/demo.gif` and
     use ![lore demo](docs/demo.gif). -->
[![asciinema demo (coming soon)](https://img.shields.io/badge/demo-asciinema-d35400?logo=asciinema)](https://github.com/jmurray2011/lore)

## Why lore

- **Auditable** — every answer cites exact chunks; `ask --verify` checks each claim is
  entailed by what it cites; `lore eval` gates retrieval and faithfulness in CI.
- **Fast** — single static binary; concurrent ingest; millisecond vector search.
- **Safe** — invariants enforced in code; no destructive op without explicit intent.
- **Automation-first** — data to stdout, logs to stderr, `--json` everywhere, meaningful exit codes.
- **Provider-agnostic** — any OpenAI-compatible endpoint (OpenAI, Azure, Ollama, vLLM, …).

```console
# prove the answer is grounded — fail CI if any claim isn't supported by its citation
$ lore ask notes "how does auth work?" --verify-strict
# measure retrieval/faithfulness over a question set; non-zero exit below threshold
$ lore eval notes -f questions.jsonl --verify --fail-under recall=0.8 --fail-under support_rate=0.9
```

Retrieval beyond plain cosine: **hybrid** BM25⊕vector (`--hybrid`), metadata
filtering (`--where 'author=alice'`), cross-encoder rerank (`--rerank`),
diversity (`--mmr`, `--max-per-source`), and **recency** time-decay ranking
(`--recency`) so newer documents aren't buried by stale-but-similar ones. See
[docs/retrieval.md](docs/retrieval.md).

**How lore is different.** Where RAG usually means a Python framework (LlamaIndex,
LangChain, txtai) or hand-rolled embedding + vector-store calls, lore ships a
**portable, citable corpus an agent can query** — grounded answers citing exact
chunks, one static binary, and a read-only [MCP server](docs/mcp.md). Build the
index once, `export` it, point an agent at it: no notebook, no service.

## Install

**Prebuilt binaries** (recommended) — every release ships static, cross-compiled
binaries for Linux, macOS, and Windows (amd64 + arm64) with checksums. Download
the archive for your platform from the
[latest release](https://github.com/jmurray2011/lore/releases/latest), then:

```console
tar -xzf lore_<version>_linux_amd64.tar.gz   # Windows ships a .zip
sudo install lore /usr/local/bin/
```

**With Go** (1.25+): `go install github.com/jmurray2011/lore/cmd/lore@latest`

**From source:** `git clone https://github.com/jmurray2011/lore && cd lore && go build -o lore ./cmd/lore`

## Quickstart

```console
# create a collection (pinned to your embedding model's space)
lore init notes

# ingest files or directories — idempotent, safe to re-run
lore add notes ./docs report.pdf
lore add notes ./docs --exclude '*(1).pdf'                  # skip files by glob
cat meeting.md | lore add notes --stdin --name meeting.md   # or pipe content in

# re-ingest changed sources later; --prune also drops docs deleted at the source
lore sync notes --prune
lore sync notes --prune --dry-run        # preview what --prune would remove

# retrieve the most similar chunks
lore query notes "rotation policy for signing keys" -k 5
lore query notes "rotation policy" --source '*.pdf'         # scope to matching docs

# ask a grounded question (retrieval + synthesis with citations)
lore ask notes "what is our key rotation policy?"
lore ask notes "…" --strict          # exit 1 if retrieval finds no chunks (empty collection, over-narrow --source/--where)
lore ask notes "…" --verify-strict   # exit 5 if any answer claim isn't supported by its citation (faithfulness gate)

# inventory & cleanup
lore ls                          # collections
lore status notes                # one collection's metadata
lore docs notes                  # documents in a collection
lore cat notes --doc report.md   # print a document's chunks (--doc takes a basename, glob, or full URI)
lore rm  notes --doc report.md   # delete a document (or `lore rm notes` for all)
lore diff old new                # document-level diff: added / removed / changed by source
```

Every command takes `--json`; human output is colorized on a TTY and plain
elsewhere (`--no-color` / `NO_COLOR=1` forces it off). Query hits and citations
are tagged `source#chunk`, so every result traces back to its document.

## Features

- **Grounded, cited answers** — every claim traces to the exact chunk it came from. [docs →](docs/retrieval.md#auditing-citations)
- **Two-stage reranking** — a cross-encoder pass, the biggest precision lever in RAG. [docs →](docs/retrieval.md#two-stage-retrieval-reranking)
- **Token-budget retrieval** — fill *N* tokens of context instead of a fixed chunk count. [docs →](docs/retrieval.md#token-budget-retrieval)
- **Streaming answers** — tokens stream on a TTY, buffer cleanly in pipes. [docs →](docs/retrieval.md#streaming-answers)
- **Cross-collection retrieval** — merge several corpora, or semantically diff two. [docs →](docs/retrieval.md#cross-collection-retrieval)
- **Portable & encrypted corpora** — `export` a whole indexed corpus to one file; ship or `age`-encrypt it. [docs →](docs/corpora.md)
- **Collection diff** — see which documents were added, removed, or changed between two collections or a snapshot. [docs →](docs/corpora.md#diffing-collections)
- **Read-only MCP server** — expose your corpora as grounded, cited tools to any MCP client. [docs →](docs/mcp.md)
- **Structure-aware chunking** — heading/paragraph-aware, code-fence-safe, pinned per collection. [docs →](docs/retrieval.md#chunking)
- **Provider-agnostic** — any OpenAI-compatible endpoint, with independent embed/chat/rerank endpoints. [docs →](docs/configuration.md#split-embedchat-endpoints)

## How it works

Documents are extracted, chunked, embedded, and stored with their vectors;
`query` runs similarity search and `ask` synthesizes a grounded answer citing the
chunks it used. lore is hexagonal (ports & adapters) with dependencies pointing
inward only, so storage engines and providers swap behind conformance-verified ports.

```
cli  ──►  app (use cases)  ──►  domain
              ▲
              │ implements ports defined in app
        adapters (memstore, sqlite, openai, fs, docx, pdf, xlsx)
```

Adding a storage engine or provider means implementing the relevant ports and
passing the conformance suites — no changes to the core.

## Configuration

Precedence: **flags > env (`LORE_*`) > config file > defaults**; the config file
is TOML at `<user-config-dir>/lore/config.toml`. Full settings reference —
provider, rerank, storage, chunking, cache, split endpoints, Azure — in
[docs/configuration.md](docs/configuration.md).

## Output and exit codes

stdout carries data (human-readable on a TTY, JSON with `--json`); logs/errors to stderr.

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | runtime error |
| 2 | usage error |
| 3 | not found |
| 4 | invariant violation (e.g. embedding-space mismatch) |
| 5 | quality gate not met (`ask --verify-strict`, `eval --fail-under`) |

## Documentation

- [Retrieval & answers](docs/retrieval.md) — diagnostics, reranking, budgets, cross-collection, streaming, chunking, citation auditing.
- [Configuration](docs/configuration.md) — env/TOML reference, global flags, cache, Azure, split embed/chat endpoints, attachments.
- [MCP server](docs/mcp.md) — tools, Claude Desktop setup, security posture.
- [Portable & encrypted corpora](docs/corpora.md) — `export`/`import` and the `age` encryption threat model.
- [Document formats](docs/formats.md) — what `lore add` ingests, and the unsupported/skipped distinction.

## Contributing

Contributions are welcome. lore is hexagonal with a strict dependency direction
(`cli → app → domain`), developed test-first, with a conformance-suite contract
keeping adapters swappable. Before sending a change, make the validate gate pass:

```console
gofmt -l .        # must print nothing
go vet ./...
go test -race -count=1 ./...
```

## License

Licensed under the [Apache License 2.0](LICENSE).
