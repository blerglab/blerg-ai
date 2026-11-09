//go:build !linux

package shellruntime

import "os"

func protectHelperProcess() error { return ErrClosed }
func helperTransport(retained bool) (*os.File, *os.File, error) {
	if retained {
		return nil, nil, ErrClosed
	}
	return os.Pipe()
}
