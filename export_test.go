package logshipperlite

// ExportForTest exports internal helpers for package tests.
var (
	DefaultSleepForTest      = defaultSleep
	ComputeRetryDelayForTest = computeRetryDelay
	ParseRetryAfterForTest   = parseRetryAfter
)
