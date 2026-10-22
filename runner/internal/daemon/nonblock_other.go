//go:build !unix

package daemon

// nonBlockFlag: no FIFOs to guard against here.
const nonBlockFlag = 0
