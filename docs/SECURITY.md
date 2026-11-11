# Security & Threat Model

## Defended

- **Log line length**: Lines are capped at `Config.MaxLineBytes` (65,536 bytes). Excess bytes are discarded to prevent memory exhaustion.
- **Backpressure**: Memory usage is bounded by `Config.MaxPendingBatches` (default 4) and batch size limits.
- **Checkpoint integrity**: Checkpoint files are validated for version 1, strict schema, and non-negative offsets. Files are written atomically using `fsync` and `os.Rename`.
- **Leakage**: Error messages and `Event` payloads are designed to exclude log line contents to prevent sensitive data leakage.

## Not defended

- **Arbitrary file access**: Relies on host OS file permissions to secure source log files.
- **Remote endpoint compromise**: The utility trusts the HTTP destination to correctly handle deduplication via `Idempotency-Key`.
