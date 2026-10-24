//go:build linux

package processcontainment

import (
	"os"
	"os/exec"
	"strings"
)

// A nonzero-to-nonzero UID change does NOT clear ambient capabilities. The
// immutable image's fixed setpriv clears ambient/inheritable sets before exec;
// no-new-privileges prevents file capabilities from restoring them. Loader
// injection is removed before either trusted helper executable is started.
func DropExecCapabilities(cmd *exec.Cmd) {
	original := cmd.Path
	cmd.Path = "/usr/bin/setpriv"
	cmd.Args = append([]string{cmd.Path, "--ambient-caps=-all", "--inh-caps=-all", "--no-new-privs", "--", original}, cmd.Args[1:]...)
	cmd.Env = HelperEnvironment(cmd.Env)
}
func HelperEnvironment(environment []string) []string {
	if environment == nil {
		environment = os.Environ()
	}
	safe := make([]string, 0, len(environment))
	for _, v := range environment {
		key, _, _ := strings.Cut(v, "=")
		if strings.HasPrefix(key, "LD_") || key == "GCONV_PATH" || key == "GLIBC_TUNABLES" {
			continue
		}
		safe = append(safe, v)
	}
	return safe
}
