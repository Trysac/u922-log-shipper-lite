// Package logshipperlite is a small, dependency-free Go library: Tail log files, parse and batch lines, and ship them over HTTP with retries, backpressure and checkpoints so restarts lose nothing
//
// The public API is every exported identifier of this root package. Functions are pure: they do
// no I/O, keep no global mutable state and report invalid input through returned errors.
package logshipperlite
