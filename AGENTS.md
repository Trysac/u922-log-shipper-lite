# AGENTS.md

Instructions for coding agents (and humans) working on `github.com/Trysac/u922-log-shipper-lite`.

## Commands

| Task | Command |
|---|---|
| Download modules | `go mod download` |
| Run every gate (must pass before a PR) | `go run ./scripts/check` |
| Tests only | `go test -cover .` |
| Format | `gofmt -w .` |
| Vet | `go vet ./...` |
| Vulnerability scan | `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` |

The gate sequence is: `gofmt -l` (must print nothing), `go vet`, `go test` (with `-race` when cgo
is available) with statement coverage >= 80 % of the library, documentation manifest
(`docs-manifest.json`), gitleaks, govulncheck. It stops at the first failure.

## Folder map

| Path | Content |
|---|---|
| `*.go` (root) | The library, package `logshipperlite`. Public API is every exported identifier. |
| `doc.go` | Package documentation shown on pkg.go.dev. |
| `*_test.go` (root) | Table-driven tests and runnable `Example` functions, in package `logshipperlite_test`. |
| `internal/` | Optional private packages; not importable by callers. |
| `cmd/` | Optional command-line entry points, one directory per binary. |
| `testdata/` | Optional fixtures and golden files used by tests. |
| `scripts/check/` | The single definition of the quality gates. |
| `docs/` | Architecture, decisions (ADRs), testing, security, project record, stack. |
| `fabrica.json` | Install and check commands used by the factory and CI. |
| `docs-manifest.json` | Documentation files the docs gate requires. |
| `.github/` | CI wrapper and Dependabot configuration. |

## Conventions

- Go >= 1.23 (the `go` directive in `go.mod`), formatted with `gofmt`, clean under `go vet`.
- Standard library only unless an ADR in `docs/decisions/` justifies a dependency.
- Every exported identifier has a doc comment; errors are returned, never panicked, and sentinel
  errors are exported so callers can use `errors.Is`.
- Write the failing test first; test through the public API from package `logshipperlite_test`.
- Every behavior change updates `README.md` usage, `CHANGELOG.md` (Unreleased) and, if it is a
  design choice, a new ADR.
- Conventional Commits (`feat:`, `fix:`, `docs:`, `test:`, `refactor:`, `chore:`).

## Limits

- Never push to `main`; work on a branch and open a pull request.
- Do NOT edit `.github/`, `fabrica.json`, `docs-manifest.json`, `scripts/`, `go.mod` or `go.sum`.
  They are the quality contract; changes to them are made by a human.
- Never lower the coverage threshold or skip a test to make a gate pass.
- Never commit secrets, tokens or `.env` files; gitleaks blocks them.
