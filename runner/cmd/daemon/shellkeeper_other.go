//go:build !linux

package main

// macOS keeper supervision is deferred; existing shell teardown is unchanged.
func shellKeeperCommand(_ []string) bool { return false }

func keeperStartup() (func(), error) { return func() {}, nil }
