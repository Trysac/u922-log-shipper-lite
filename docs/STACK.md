# Stack

## Used

| Tool | Why |
|---|---|
| Go >= 1.23 | Oldest supported release line; one toolchain covers build, test and format. |
| Go modules | Built-in dependency management with checksum verification (`go.sum`). |
| gofmt + go vet | The canonical formatter and static checks; no configuration to maintain. |
| go test (race detector, cover) | Built-in tests, runnable examples, race detection and coverage. |
| govulncheck | Official scanner that reports only vulnerabilities reachable from the code. |
| gitleaks | Secret scanning locally and in CI. |
| GitHub Actions + Dependabot | Thin CI wrapper over the local gates; weekly dependency updates. |

## Discarded

| Tool | Why not |
|---|---|
| golangci-lint | Large external tool; `go vet` is enough for a small library. Revisit via ADR. |
| testify | The standard `testing` package keeps the module dependency-free. |
| Makefile / shell scripts for gates | Not portable to Windows; the gates are a stdlib Go program. |
