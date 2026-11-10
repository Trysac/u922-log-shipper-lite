# Synchronous Step Engine with First-Line SHA-256 Fingerprints and Atomic Checkpoints

Status: accepted (generated, pending human review)

## Context

Small services need an embeddable log shipper that survives process restarts, log rotations, and collector outages without losing lines, unbounded memory growth, or background goroutine leaks.

## Decision

Implement a synchronous Step(ctx) state machine backed by injected Sleep and HTTP Client dependencies. Identify files by the first 16 hex characters of the SHA-256 of their first complete line, bound unread memory via MaxPendingBatches and MaxLineBytes, drain open handles across rename rotations, and persist checkpoints via write-temp, fsync, and atomic rename only after 2xx responses.

## Consequences

Callers can drive shipping deterministically in tests without real clocks or sleeps, memory usage is strictly bounded during collector outages, and pairing with a key-deduplicating HTTP receiver guarantees exact-once log delivery across crashes.
