# Testing

## Running

```sh
go test -cover .          # tests with coverage summary
go run ./scripts/check    # every gate, as CI and the factory run it
```

## Policy

- Statement coverage of the library (every package except `scripts/`) must stay at or above 80 %;
  the gate fails below it.
- Tests live in package `logshipperlite_test` and exercise the public API only. They assert every
  observable effect: return values, returned errors (`errors.Is`) and their messages.
- Prefer table-driven tests with `t.Run` subtests; document usage with runnable `Example`
  functions, which `go test` verifies through their `// Output:` comments.
- The race detector runs whenever cgo and a C compiler are available (always in Linux CI); the
  gate prints why when it has to run without it.
- Each bug fix starts with a failing test that reproduces it.
- Tests are deterministic: no network, no wall clock, no dependence on test order.

## Tiers

- Local tier (every gate run, coverage measured here): unit and property tests, plus conformance on small
  fixtures kept in the repository.
- CI tier: conformance that needs an external tool or a download. Start the file with `//go:build conformance`; `scripts/check` adds `-tags=conformance` only in CI
  (unless `FABRICA_CI=1`). The CI `conformance` job sets `FABRICA_CI=1` and runs the full suite. Tools are installed
  by the optional `ci/conformance-setup.sh` (bash, CI only; verify every download against a pinned SHA-256), and
  tagged tests skip cleanly when their tool is missing.

## Layout

`<file>_test.go` sits next to the file it tests. Fixtures and golden files go in `testdata/`.
