# Changelog

All notable changes to api-projection are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Pre-1.0:
minor bumps for additive surface, patch bumps for fixes.

## Unreleased

## [0.1.0] - 2026-09-18

### Added

- Initial release: mechanical API-to-MCP projection in two stages.
- `manifest`: the manifest schema and its security invariants.
- `compiler` and `cmd/api-projection-compile` (Stage A): resolves a
  human-authored selection against an OpenAPI fragment and emits a reviewed
  manifest, refusing rather than defaulting past any selection gap.
- `interpreter` (Stage B): serves a manifest's tools as an MCP server, one
  process per manifest, with an adversarial hardening test suite.
- `credential`: host-side credential-reference resolution, kept separate from
  the interpreter.
- A read-only GitHub releases pilot as a worked example.

[Unreleased]: https://github.com/hollis-labs/api-projection/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/hollis-labs/api-projection/releases/tag/v0.1.0
