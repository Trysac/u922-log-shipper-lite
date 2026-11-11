# Testing

Run the full test suite and quality gates with:

```bash
go run ./scripts/check
```

## Test Tiers

- **Acceptance tests (`logshipperlite_test.go`)**: Verifies NDJSON payloads, SHA-256 fingerprint generation, partial trailing lines, exponential backoff (including `Retry-After`), queue backpressure logic, file rotation draining, line truncation behavior, and correct error code returns.
- **Property-based fault injection (`TestAcceptance4Property200Seeds`)**: Utilizes 200 deterministic PCG seeds to simulate random network transport failures, dropped HTTP acknowledgements, and process crashes (restarting the shipper from disk checkpoints). Asserts that a key-deduplicating receiver ends with every line present exactly once, in original file order.
