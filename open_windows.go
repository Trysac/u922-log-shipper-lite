//go:build windows

package logshipperlite

import (
	"os"
	"syscall"
)

func openLogFile(name string) (*os.File, error) {
	if len(name) == 0 {
		return nil, &os.PathError{Op: "open", Path: name, Err: syscall.ERROR_FILE_NOT_FOUND}
	}
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	access := uint32(syscall.GENERIC_READ)
	sharemode := uint32(syscall.FILE_SHARE_READ | syscall.FILE_SHARE_WRITE | syscall.FILE_SHARE_DELETE)
	var sa *syscall.SecurityAttributes
	createmode := uint32(syscall.OPEN_EXISTING)
	flags := uint32(syscall.FILE_ATTRIBUTE_NORMAL)

	handle, err := syscall.CreateFile(p, access, sharemode, sa, createmode, flags, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	return os.NewFile(uintptr(handle), name), nil
}
