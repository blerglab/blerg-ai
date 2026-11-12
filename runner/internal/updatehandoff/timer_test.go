package updatehandoff

import "testing"

func TestTimerUnit(t *testing.T) {
	for _, tc := range []struct{ command, want string }{
		{"systemd-run --user --unit=reinstall --on-active=5s install/desktop/daemon/install.sh install; while ! test -f /tmp/reinstall.done; do sleep 1; done; cat /tmp/reinstall.done", "reinstall.service"},
		{"systemd-run --user --unit reinstall.service --on-active=5s install/desktop/daemon/install.sh install", "reinstall.service"},
		{"systemd-run --unit=reinstall --on-active=5s install/desktop/daemon/install.sh install", ""},
		{"systemd-run --user --unit=$name --on-active=5s install/desktop/daemon/install.sh install", ""},
		{"systemd-run --user --unit=reinstall install/desktop/daemon/install.sh install", ""},
		{"systemd-run install/desktop/daemon/install.sh install --user --unit=reinstall --on-active=5s", ""},
		{"systemd-run --user --unit=a --on-active=5s install/desktop/daemon/install.sh install; systemd-run --user --unit=b --on-active=5s install/desktop/daemon/install.sh install", ""},
	} {
		if got := TimerUnit(tc.command); got != tc.want {
			t.Errorf("%q: %q, want %q", tc.command, got, tc.want)
		}
	}
}
