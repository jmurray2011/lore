# Portable & encrypted corpora

A built collection is a self-contained, queryable knowledge artifact — so build
the index once and *ship* it. This page covers `export`/`import` and the
optional `age` encryption envelope.

## Portable corpora

`lore export` serializes a whole collection into a single, versioned file;
`lore import` reconstructs it anywhere, pins intact:

```bash
lore export kb -o kb.lore                 # one file: chunks, vectors, pins, metadata
lore import kb.lore                        # reconstruct locally (same name)
lore import kb.lore --name kb-staging      # …or under a new name
lore import kb.lore --force                # overwrite an existing collection
```

The artifact carries the embedding-space pin (model + dimensions) and the
chunker pin, so the reconstructed collection enforces the same invariants on
later `add`/`query` — the round-trip is lossless (a query against the imported
collection returns the same results as the original). The format is versioned;
an artifact newer than your `lore` understands is refused with a clear error
rather than mis-parsed.

This is what you commit to a repo, attach to a release, or hand a teammate (the
docs, pre-indexed), and it is the natural unit to point an agent or
[MCP server](mcp.md) at. The artifact records each document's source URI
(including local file paths) alongside its chunks. What a recipient needs to
query it is below.

### What the recipient needs

`import` itself needs nothing but the `lore` binary, and `ls`/`status`/`docs`/
`cat`/`diff` then work fully offline. Querying is where the embedding space
matters — the query text has to be embedded into the same space as the stored
vectors:

- **The same embedder.** To run `query`/`ask` on the carried vectors, configure an
  OpenAI-compatible endpoint (and key) serving the *same embedding model and
  dimensions* the corpus was built with. `lore status <name>` after import shows
  that model/dimensions; a mismatch is refused with a clear, actionable error.
- **A different embedder** (`import --re-embed`). Rebuilds the vectors from the
  carried chunk text with *your* configured embedder and pins the collection to
  your space, so any embedder you can run — including a local one like Ollama's
  `nomic-embed-text` — makes the corpus queryable. It calls the embedder, so cost
  and time scale with the corpus; it is a one-time step.
- **No embedder at all** (`query --lexical` / `ask --lexical`). Retrieve by BM25
  keyword match with no embedding, so a handed corpus is usable with no API key.
  `ask --lexical` still needs a chat model to synthesize, but any chat endpoint
  works — chat is not tied to the corpus's embedding space.

The lossless round-trip (identical results to the original) holds on the first
path — same embedder, carried vectors. `--re-embed` re-derives vectors in a new
space, so results are equivalent, not byte-identical.

## Diffing collections

`lore diff <from> <to>` reports the document-level difference between two
collections — which sources were **added**, **removed**, or **changed** —
comparing by source URI and per-document content hash:

```bash
lore diff kb kb-staging                    # human: added / removed / changed sections
lore diff kb kb-staging --json             # {from, to, added[], removed[], changed[]}
```

It compares *what was ingested*, not how it was embedded, so it works across
collections pinned to different embedding spaces. That makes "snapshot before you
mutate" a two-step workflow: export the collection under a snapshot name, re-ingest
or edit sources, then diff against the snapshot to see exactly what moved.

```bash
lore export kb -o kb.lore && lore import kb.lore --name kb-snapshot
# …re-ingest / edit sources…
lore diff kb-snapshot kb
```

Because the comparison keys on source URI, a renamed file shows as a paired
removal + addition — the silent-orphan case `add` alone can't surface. The
`changed` bucket carries both content hashes (`from`/`to`) in `--json`. Exit
status is `0` whether or not the collections differ (a difference is data, not an
error).

## Encrypted corpora

`lore export --encrypt` wraps the artifact in an [age](https://age-encryption.org)
envelope. **Read the threat model before relying on it:**

- Encryption protects the **exported artifact at rest and in transit** —
  committing it, handing it over, backing it up to the cloud.
- It does **not** protect the working database during use: `import` decrypts to
  a normal plaintext working DB that lore queries; plaintext exists on disk while
  the collection is in use.
- **A lost key or passphrase means the corpus is permanently unrecoverable.**
  There is no recovery path. Back up the key separately from the artifact.
- The **entire artifact is encrypted, including the embedding vectors** — vectors
  are invertible to approximate plaintext, so lore never encrypts text while
  leaving vectors in the clear. It is whole-artifact or nothing.

The automation-safe, recommended way to supply the passphrase is
`--passphrase-cmd` (or `LORE_EXPORT_KEY_CMD`): lore runs the command and reads the
passphrase from its stdout, so the secret never lands in a flag value, in argv,
or on disk.

```bash
# Encrypt with a passphrase from your secret manager (never in shell history):
lore export kb -o kb.lore.age --encrypt --passphrase-cmd 'op read op://vault/lore/passphrase'
lore import kb.lore.age --passphrase-cmd 'op read op://vault/lore/passphrase'

# Or encrypt to a teammate's age public key; they import with their identity:
lore export kb -o kb.lore.age --encrypt --recipient age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p
lore import kb.lore.age --identity ~/.age/key.txt
```

On an interactive terminal with no command source, lore prompts for the
passphrase (twice on export, once on import; never echoed). With no key source
**and** no terminal — a script, a pipe — encryption/decryption fails with a usage
error rather than hanging. `--recipient`/`--identity` and the passphrase path are
mutually exclusive. Encryption is detected from the artifact's content, not its
file name, so a `.lore` that is encrypted (or a `.lore.age` that isn't) just
works; a wrong key or a tampered artifact fails to decrypt rather than producing
garbage.

The artifacts are ordinary `age` files: anyone with the key can decrypt them with
the standalone [`age`](https://age-encryption.org) CLI — no lore lock-in.
