//go:build !linux

package main

import "errors"

func newClock() (clock, error) {
	return nil, errors.New("record requires Linux (run inside the WSL guest); read is portable")
}
