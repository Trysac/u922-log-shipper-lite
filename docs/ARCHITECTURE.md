# Architecture

## Overview

`logshipperlite` is organized as a deterministic state machine driven by `(*Shipper).Step(ctx)`:

1. **Read & Batch Phase**: While the number of sealed pending batches is below `MaxPendingBatches` (default 4), the shipper inspects each configured path. Complete newline-terminated lines are read using `ReadAt` without modifying file descriptors shared across rotations. Lines longer than `MaxLineBytes` (default 65,536 bytes) are capped at `MaxLineBytes`, tagged with `"truncated":true`, and the remainder of that line is skipped.
2. **Fingerprinting & Rotation**: A file's identity is the first 16 hex characters of the SHA-256 digest of its first complete line (including the trailing `\n`). Files without a complete first line are not read yet. When a configured path is replaced (new inode or changed first-line fingerprint) or truncated below the read offset, the shipper drains any remaining complete lines from the previously open handle until EOF before starting the new file at offset `0`.
3. **Send & Retry Phase**: `Step` sends the oldest pending batch via HTTP `POST` (`Content-Type: application/x-ndjson`, `Idempotency-Key: <hex sha256(firstKey|lastKey)>`).
   - `2xx`: Removes the batch from the pending queue and atomically writes the checkpoint.
   - `408`, `429`, `5xx`, or transport error: Retries up to `MaxRetries` (default 10) within the `Step` using `min(30s, 100ms * 2^(n-1))` or the server's `Retry-After` header (seconds or HTTP-date, capped at 30s). If retries are exhausted, `Step` returns `*Error{Code: "retries_exhausted"}` and keeps the batch at the head of the queue.
   - Other `4xx`: Immediately returns `*Error{Code: "rejected"}` and keeps the batch pending.
