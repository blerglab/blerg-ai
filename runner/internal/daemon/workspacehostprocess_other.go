//go:build !linux

package daemon

import (
	"context"
	"errors"
)

type hostHelperIdentity struct {
	PID           int
	Start, Boot   string
	Device, Inode uint64
}

func hostHelperIdentityFor(int) (hostHelperIdentity, error) {
	return hostHelperIdentity{}, errors.New("host cleanup identity unsupported")
}
func reapHostHelper(context.Context, hostHelperIdentity) error {
	return errors.New("host cleanup proof unsupported")
}

func hostControllerGone(hostHelperIdentity) error {
	return errors.New("host cleanup proof unsupported")
}
func hostWriterGroupFor(string, func(int) error) hostWriterGroup { return nil }
func ServeHostWriter([]string) int                               { return 125 }
