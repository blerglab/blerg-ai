//go:build unix

package daemon

import "syscall"

// nonBlockFlag opens without blocking on a FIFO (readBounded checks the handle is a regular file).
const nonBlockFlag = syscall.O_NONBLOCK
