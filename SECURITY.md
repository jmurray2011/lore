# Security policy

## Reporting a vulnerability

Please report security issues privately, not as a public issue.

Use GitHub's [private vulnerability
reporting](https://github.com/jmurray2011/lore/security/advisories/new) on this
repository. Include the lore version (`lore --version`), your platform, and the
smallest reproducer you can manage.

I maintain lore on my own time, so I do not promise a response SLA. I aim to
acknowledge a report within a week and to fix confirmed issues in the next
release. If a report leads to a fix, I will credit you in the release notes
unless you ask me not to.

## Supported versions

Fixes land on the latest minor release. Older tags are not patched.

## Scope

lore is a local CLI. It has no server component you are expected to expose, no
account system, and no telemetry. The parts worth attacking are the places where
untrusted bytes enter:

**In scope**

- **Document ingest.** `add`/`sync` parse attacker-influenceable files (PDF,
  DOCX, XLSX, Markdown, text). Parser crashes, path traversal out of the source
  root, resource exhaustion, or anything reaching outside the collection.
- **Portable artifacts.** `import` reconstructs a collection from a file that may
  have come from someone else. Malformed or hostile artifacts must produce a
  clean error, not memory exhaustion or arbitrary writes.
- **MCP server.** `lore mcp --http` binds loopback by default and refuses a
  non-loopback bind without a bearer token. Bypassing that gate, or the
  cross-origin protection wrapping it, is in scope.
- **Secret handling.** API keys and export passphrases must not land in argv, in
  logs, or in error output. A leak there is a real finding.
- **Provider transport.** Anything that lets a malicious endpoint response
  compromise the client.

**Out of scope**

- **Prompt injection through document content.** Retrieved chunks flow into the
  calling model's context and may contain injected instructions. lore does not
  interpret chunk text as instructions and cannot sanitize it. Treating tool and
  answer output as untrusted is the operator's responsibility - see
  [docs/mcp.md](docs/mcp.md). Scope what an agent can reach with `--collections`.
- **LLM output quality.** A wrong, unfaithful, or unhelpful answer is a
  correctness bug, not a vulnerability. File it as an issue. (`ask --verify`
  and `lore eval` exist to measure exactly this.)
- **Attacks requiring an already-compromised local machine.** lore trusts your
  filesystem, your config file, and your environment. Someone who can read
  `~/.config/lore/config.toml` already has your API key.
- **Exposing `lore mcp --http` on an untrusted network deliberately.** The bearer
  token is a gate, not a hardened auth system. Do not put it on the public
  internet.
- **Vulnerabilities in upstream dependencies** with no lore-specific exploit
  path. Report those upstream. CI runs `govulncheck` on every change.

## Hardening already in place

Useful context if you are looking for something new rather than re-finding what
is handled:

- Bounded reads at every untrusted-input boundary via `internal/limitio`, which
  fails loudly (`ErrTooLarge`) instead of silently truncating: DOCX/XLSX zip-bomb
  guards (declared-size fast-fail plus a streaming bound), artifact decode, file
  ingest, and CLI attachments.
- `lore mcp --http` is loopback-only unless a bearer token is set, and is wrapped
  in the stdlib cross-origin protection handler.
- Export passphrases resolve through `--passphrase-cmd` so the secret never
  appears in a flag value or in shell history.
- Pure-Go build, `CGO_ENABLED=0`, no cgo dependencies.
