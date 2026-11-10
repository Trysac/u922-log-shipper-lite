// Package logshipperlite tails log files, parses and batches complete lines
// into newline-delimited JSON (NDJSON), and ships them over HTTP with retries,
// bounded memory backpressure, rotation handling, and atomic checkpoints.
//
// Delivery on the wire is at-least-once. Every shipped record includes a
// deterministic key (<16-hex-char first-line SHA-256 fingerprint>:<byte offset>),
// and every HTTP request carries an Idempotency-Key header derived from the
// batch's first and last record keys. When paired with a receiver that
// deduplicates by record key, end-to-end delivery is effectively exactly-once.
package logshipperlite
