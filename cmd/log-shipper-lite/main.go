// Command log-shipper-lite tails log files and ships NDJSON batches over HTTP.
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/Trysac/u922-log-shipper-lite"
)

func main() {
	os.Exit(Run(os.Args[1:], os.Stdout, os.Stderr))
}

// Run executes the CLI with the provided arguments and output streams.
func Run(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return logshipperlite.RunCLI(ctx, args, stdout, stderr)
}
