//go:build !linux && !darwin

package discovery

import "os"

func openBeneath(_ *os.File, _ string, _ int) (*os.File, error) { return nil, errUnavailable }
func instanceIdentity(_ *os.File, _ os.FileInfo) string         { return "" }
func metadataVersion(os.FileInfo) string                        { return "" }
