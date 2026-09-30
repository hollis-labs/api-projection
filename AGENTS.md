# api-projection

Mechanical API-to-MCP projection: `manifest` (the schema and its
invariants), `compiler` (Stage A), `interpreter` (Stage B), `credential`
(host-side credential-reference resolution). See README.md for the package
map. The design this implements is recorded in the project's API-to-MCP
projection ADR; its numbered decisions are cited below.

## Start Here

- README.md covers the two-stage pipeline and the pilot worked example.
- `manifest/manifest.go` owns the schema's own security invariants —
  read this before touching anything else; both `compiler` and
  `interpreter` build on it rather than re-deriving any of it.
- `interpreter/adversarial_test.go` is the ADR-required hardened test:
  a populated secret must never survive the response allow-list, checked
  against hostile manifests. Any change to `interpreter/interpreter.go`'s
  response mapping (`mapResponse`/`extractFields`) needs this file to stay
  green, not just the happy-path tests.

## Commands

```bash
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

No CI, no Makefile — same convention as `libs/mcp-host`/`libs/go-mcp`.

## Boundaries

**The interpreter never imports `credential`.** Per the ADR's decision 4,
credential resolution is the host's job (`apps/station`), never the
interpreter's — the interpreter only ever reads one environment variable,
named by `Manifest.Credential.Env`. Adding a credential-store import to
`interpreter/` would reproduce exactly the design this repo exists to
avoid; if a change seems to need it, the resolution step belongs in the
host instead.

**Response fields are flat literal paths, enforced at `manifest.Load`/
`Parse` time, not left to interpreter discipline.**
`manifest.validateFlatFieldPath` refuses wildcards, array indexing, and
empty segments structurally — a manifest that would need one to express
its intent should not be authored, not have the check relaxed. `compiler`
validates each field path exists in the real OpenAPI definition on top of
this; the two checks are independent and both stay in place.

**Stage A never invents an allow-list decision.** `compiler.Compile`
refuses (an unknown `operation_id`, a field that isn't a declared
parameter, an upstream-required parameter neither allow-listed nor
pinned, a response field not in the declared schema) rather than
defaulting past a selection gap. A human's selection file is the one place
allow-list decisions get made; loosening a refusal into a default here
would move that decision into code no code reviewer would think to look at.

**Stage B spawns one process per manifest** (`apps/station`'s
`process`-mode logical server, one per pilot/API), never one interpreter
process serving multiple manifests. This is what makes credential
isolation structural rather than something the code has to enforce — see
the ADR's decision 2. Don't consolidate multiple manifests into one running
interpreter process without revisiting that call first.

**One credential-capability reference per manifest, v1.** An API needing
distinct credentials per operation isn't handled — `manifest.Credential`
is a single field, not a per-tool one. Named limitation, not an oversight;
see the ADR's "Negative / accepted risk" section before changing this.

**No verified caller identity flows through any of this yet.** Audit/budget
on a projected tool call is only as honest as whatever caller identity
`libs/mcp-host`'s serving layer has today, which is not verified
as of this writing. Don't claim a
stronger identity guarantee than that in a manifest's description or a
tool's own text.

**`libs/mcp-host` needs no changes for this to work** — a projected API is
an ordinary `process`-mode logical server, spawned over the transport
interface that already exists. If a change here starts looking like it
needs a new mcp-host capability, that's a sign the design has drifted from
the ADR's decision 3, not a sign mcp-host needs to grow one.
