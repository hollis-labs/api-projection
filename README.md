# api-projection

api-projection mechanically projects an approved API's operations as MCP
tools, from a reviewed manifest rather than hand-written per-API code. See
the ADR at `project/atlas/knowledge/adr/adr_api_to_mcp_projection`
(Tesseract) for the full design; this README covers what's actually built.

Built for ATLAS-FOLLOWUP-013's pilot (Torque `CW-20260918-0054`), validating
the architecture `CW-20260918-0038` designed against one real API (GitHub,
read-only) before any broader adoption decision.

## Two stages

**Stage A — compiler** (`compiler/`, `cmd/api-projection-compile`). Offline,
tool-assisted, human-reviewed: resolves a small human-authored *selection*
(which operation, which fields, which values are pinned vs. caller-settable)
against a real OpenAPI document fragment, and emits a *manifest* — the
artifact that's actually reviewed and git-tracked. The compiler validates
consistency (every field genuinely exists in the API definition, every
upstream-required parameter is allow-listed or pinned) and derives the
mechanical parts (JSON Schema types, heuristic go-mcp annotation hints from
HTTP method semantics); it never invents an allow-list decision itself.

```bash
go run ./cmd/api-projection-compile \
  -doc pilots/github-releases/openapi-fragment.yaml \
  -selection pilots/github-releases/selection.yaml \
  -out pilots/github-releases/manifest.yaml
```

**Stage B — interpreter** (`interpreter/`, `cmd/api-projection-server`). A
real, standalone process-mode MCP server (`go-mcp/server`, stdio) that
loads one manifest and serves its tools: builds each upstream request from
allow-listed inputs only, injects a credential from the environment
variable the manifest names (never touches the credential store itself),
and maps every response through the manifest's flat, literal-path
allow-list before it becomes a tool result — structural exclusion, not a
runtime sanitizer pass. See `interpreter/adversarial_test.go` for the
hardened "a populated secret never survives the allow-list" tests ADR 0003
requires, run against hostile manifests, not just the pilot's own.

```bash
./bin/api-projection-server -manifest pilots/github-releases/manifest.yaml
```

## manifest — the schema itself

`manifest/manifest.go` owns the schema and its own structural invariants
(never trusted to compiler or interpreter discipline alone): an
allow-listed operation, allow-listed input/response fields, flat literal
response paths only (no wildcards, no dynamic traversal), and a credential
field that must be a `keychain://`/`helper://` reference, never a literal
secret. `Load`/`Parse` both validate.

## credential — resolving a manifest's credential reference

`credential/` resolves `keychain://<authority>/<path>` and
`helper://<helper>/<path>` references, mirroring Cerberus's
`internal/secretref` (same grammar, same helper-delegation shape) under
its own OS keychain service, `"api-projection"` — distinct from Cerberus's
(`"cerberus"`) and Tether's (`"tether"`), since a projected API's
credential is neither an AI provider key nor a Cerberus-managed service
secret.

Per `adr_api_to_mcp_projection` decision 4, **the interpreter never imports
this package.** Resolution is the host's job — `apps/station`'s
`internal/secretref.Resolve` calls it before spawning a process-mode
logical server, then hands the resolved value to the spawned process over
its own `env`, keyed by the name the manifest declares
(`Credential.Env`). The interpreter only ever reads that one environment
variable.

`cmd/api-projection-cred` is the ahead-of-time provisioning CLI — UX
mirrors Tether's `mux-apikey-helper`:

```bash
printf '%s' "$TOKEN" | ./bin/api-projection-cred set keychain://api-projection/github-pilot
./bin/api-projection-cred resolve keychain://api-projection/github-pilot   # verify, prints the secret
```

## pilots/github-releases — the worked example

The pilot this repo was built to validate: GitHub's `GET
/repos/{owner}/{repo}/releases`, read-only, projected as `list_releases`.
`openapi-fragment.yaml` is a trimmed slice of GitHub's real API definition
(field names spot-checked against a live call); `selection.yaml` is the
human allow-list decision; `manifest.yaml` is the compiled, git-tracked
output actually loaded by `apps/station` (`station.yaml`'s `github-pilot`
logical server).

## Commands

```bash
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

No CI, no Makefile — same convention as `libs/mcp-host`/`libs/go-mcp`.

## Status

**Not released.** This is a validated pilot (one API, read-only, one human
operator), not a product with consumers — `apps/station` is its only
integration, itself a prototype. Public repo from the start, same as
`libs/mcp-host`/`apps/station`: built in the open, not gated behind a
release announcement.

So **release readiness is a direction, not a phase.** Security, testing,
and hardening beyond what a one-API pilot needs are ordinary work competing
on merit with everything else, sequenced by Chrispian's direction, not a
checklist gating a launch date. The generic response-allow-list path
(`interpreter/interpreter.go`) is exactly the kind of code that gets
tightened *at the first real second consumer* — a second projected API —
not preemptively for a hypothetical one; see
`~/dev/projects/agent-setup/docs/what-a-check-may-assert.md`.

**Where this stops.** Not licence to skip verification. The manifest's
allow-list is the security boundary this whole design exists around, and
it gets the real treatment now, not later — see the adversarial tests in
`interpreter/adversarial_test.go`. What changes with "not released" is
scope and hardening depth, never whether the boundary itself is tested.

Tagged at `v0.1.0`. There is no tracked or ambient `go.work`; local
cross-module development against an unreleased change is a throwaway
`go.work` outside the repos, per the portfolio convention — never a
`replace`, never a tracked workspace file.
