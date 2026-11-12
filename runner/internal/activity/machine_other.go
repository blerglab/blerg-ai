//go:build !linux

package activity

func SampleMachine(session, path string) *Machine { return nil }
