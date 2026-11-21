//go:build linux

package processtelemetry

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// ReadImage runs off the exec path. Only the classifier's small allowlist is
// returned. A bounded argv prefix is inspected transiently for known selectors.
func ReadImage(pid int) (string, string) {
	root := "/proc/" + strconv.Itoa(pid)
	exe, err := os.Readlink(root + "/exe")
	if err != nil {
		return "", ""
	}
	if program, class := Classify(exe, nil); program != "" {
		return program, class
	}
	name := filepath.Base(exe)
	if name != "go" && name != "node" && name != "nodejs" && name != "npm" && name != "npm-cli.js" {
		return "", ""
	}
	f, err := os.Open(root + "/cmdline") //nolint:gosec // Integer kernel PID under the fixed proc root, never a caller path.
	if err != nil {
		return "", ""
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return "", ""
	}
	pieces := bytes.Split(raw, []byte{0})
	argv := make([]string, 0, min(len(pieces), 64))
	for _, piece := range pieces {
		if len(argv) >= 64 {
			break
		}
		argv = append(argv, string(piece))
	}
	return Classify(exe, argv)
}
