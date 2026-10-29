//go:build !linux && !darwin

package updatehandoff

import (
	"errors"
	"net"
)

func ProcessStart(int) string      { return "" }
func ancestors(int) map[int]string { return nil }
func processSession(int) string    { return "" }
func peerPID(*net.UnixConn) (int, error) {
	return 0, errors.New("update ancestry is unavailable on this platform")
}
