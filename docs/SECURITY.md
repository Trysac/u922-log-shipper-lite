# Security notes

## Threat model

`github.com/Trysac/u922-log-shipper-lite` is a pure library without I/O. The main risks are:

- **Supply chain**: a compromised dependency or CI action. Mitigation: standard library only,
  `go.sum` verification, `govulncheck` in the local gates, Dependabot weekly updates, actions
  pinned by commit SHA, minimal workflow permissions.
- **Leaked secrets**: tokens committed by mistake. Mitigation: `gitleaks` runs in the local gates
  and in CI; `.env` files are git-ignored.
- **Insecure code**: disabled TLS verification, weak crypto, command injection. Mitigation: a
  `security` gate (gosec, high severity fails, `#nosec` ignored); fix the code instead of silencing
  it.
- **Untrusted input**: callers may pass hostile strings. Mitigation: validate input and return
  explicit errors; never use `unsafe` or `os/exec` with untrusted data.

## Publishing

Releases are git tags served through the Go module proxy and checksum database. No registry token
is stored in the repository.

To report a vulnerability, see [SECURITY.md](../SECURITY.md).
