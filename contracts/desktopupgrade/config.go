package desktopupgrade

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// DaemonDirectory follows the installer's environment-over-.env precedence for
// the one configuration key the upgrade worker needs. It never evaluates shell.
func DaemonDirectory(root, home, override string) (string, error) {
	value := override
	if value == "" {
		// #nosec G304 -- Fixed installer settings path under the locally resolved Git root.
		raw, err := os.ReadFile(filepath.Join(root, "install/desktop/.env"))
		if err != nil {
			return "", errors.New("desktop settings are unavailable")
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if !strings.HasPrefix(line, "BLERG_RUNNER_DAEMON_STATE_DIR=") {
				continue
			}
			value = strings.TrimPrefix(line, "BLERG_RUNNER_DAEMON_STATE_DIR=")
			if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
				value = value[1 : len(value)-1]
			}
			break
		}
	}
	if value == "" {
		value = filepath.Join(home, ".blerg-runner-daemon")
	}
	if !filepath.IsAbs(value) {
		return "", errors.New("daemon state directory must be absolute")
	}
	return value, nil
}
