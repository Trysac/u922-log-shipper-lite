# Project record

## Objective

Tail log files, parse and batch lines, and ship them over HTTP with retries, backpressure, and checkpoints to ensure no logs are lost on restart.

## Scope

- In scope: The public Go API (`New`, `Step`, `Run`), CLI utility, NDJSON batch formatting, per-line fingerprinting/deduplication keys, file rotation handling, atomic checkpoints, and documented error codes.
- Out of scope: Multiline log joining, glob-based directory discovery, compression, TLS client certificates, copy-truncate log rotation detection beyond simple shrinking, and Windows-specific file sharing modes.

## Timeline

| Date | Event |
|---|---|
| 2026 | Repository created from the factory utility template. |

## Effort metrics

Measured values only; a value that has not been measured stays "pending measurement".

| Metric | Value |
|---|---|
| Tokens (input / output) | 3110921 / 34097 |
| Hours (human) | pending measurement |
| Cost (USD) | 1.5686 |

## Factory run u922 (measured from API usage)

| Date (UTC) | Phase | Model | Batch | Input | Output | Cache write | Cache read | Cost (USD) |
|---|---|---|---|---|---|---|---|---|
| 2026-11-10 | generation | google/gemini-4-argon | yes | 5392 | 24304 | 0 | 0 | 0.5076 |
| 2026-11-10 | fix-2 | google/gemini-3.8-flash | no | 324737 | 1340 | 0 | 592701 | 0.2930 |
| 2026-11-10 | fix-3 | google/gemini-3.8-flash | no | 399747 | 4206 | 0 | 688641 | 0.3672 |
| 2026-11-10 | fix-4 | google/gemini-3.8-flash | no | 263806 | 1081 | 0 | 399722 | 0.2319 |
| 2026-11-10 | audit | google/gemini-3.8-flash | no | 190812 | 777 | 0 | 240852 | 0.1641 |
| 2026-11-10 | docs | google/gemini-3.1-flash-lite | no | 4511 | 2389 | 0 | 0 | 0.0047 |

Total: $1.5686. Prices: vertex line, as measured by its adapter. Providers serve the current version of each model id, so the date tells which version answered; versions, launch stages and price sources per line are in the factory's model register (docs/MODELS.md).
