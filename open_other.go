//go:build !windows

package logshipperlite

import "os"

func openLogFile(name string) (*os.File, error) {
	return os.Open(name)
}
