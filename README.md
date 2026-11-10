# log-shipper-lite

Tail log files, parse and batch lines into NDJSON, and ship them over HTTP with retries, backpressure, rotation handling, and durable checkpoints.

## Why log-shipper-lite?

Small services often ship logs with ad-hoc scripts that either re-send everything after a restart or silently drop lines when the collector is down. Full agents such as Vector or Fluent Bit can be heavy to deploy and configure when all you need is to ship a handful of files to an HTTP endpoint. `logshipperlite` is a zero-dependency, embeddable Go package and CLI with a clearly specified and property-tested delivery guarantee.

## Delivery Guarantee

Delivery on the wire is **at-least-once**: a batch stays in memory and the on-disk checkpoint does not advance until the HTTP destination acknowledges with a `2xx` status. Every record carries a deterministic `key` (`<16-char first-line SHA-256 hex>:<byte offset>`), and every HTTP POST carries an `Idempotency-Key` header (`SHA-256(<first_key>|<last_key>)`). When paired with a receiver that deduplicates by `key`, end-to-end delivery is **exactly-once**.

## Installation

```bash
go get github.com/Trysac/u922-log-shipper-lite
```

## Usage

```go
s, err := logshipperlite.New(logshipperlite.Config{
	Paths:          []string{"/var/log/app/a.log"},
	CheckpointPath: "/var/lib/app/checkpoint.json",
	Endpoint:       "https://logs.example.com/ingest",
})
if err != nil {
	panic(err)
}
_ = s.Step(ctx)
```

## Public API Summary

- **Functions**: `New(cfg Config) (*Shipper, error)`, `Step(ctx) error` (executes one sync read/send cycle), `Run(ctx) error` (main loop), `Stats() Stats`.
- **Config Options**: `BatchMaxRecords` (500), `BatchMaxBytes` (1MB), `MaxPendingBatches` (4), `MaxLineBytes` (65,536), `PollInterval` (1s), `MaxRetries` (10).
- **Error Codes**: `rejected` (permanent 4xx), `retries_exhausted` (transient failure limit reached), `bad_checkpoint`, `bad_config`, `io`.
- **Why not Vector or Fluent Bit**: This utility provides a zero-dependency, embeddable Go package, making it suitable for simple, single-purpose deployments where heavy agent orchestration is unnecessary.

See [docs/SECURITY.md](docs/SECURITY.md) for security information.
