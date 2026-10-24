//go:build !linux && !darwin

package discovery

import "os"

func openConfiguredRoot(string) (*os.File, error) { return nil, errUnavailable }
func fileIdentity(os.FileInfo) (uint64, uint64)   { return 0, 0 }
func regularPrivateMetadata(os.FileInfo) bool     { return false }

const directoryFlags = 0
const regularFlags = 0
