package daemon

import "testing"

// TestBuildModeSyncSequences verifies the pure escape-sequence construction from
// pane mode flags, independent of tmux: application-cursor-keys (DECCKM) and
// cursor-visibility sequences reflect the queried flags, and an alt-screen pane
// (alternate_on) emits nothing — alt-screen apps repaint fully on their own, so a
// mode-sync preamble would only fight them.
func TestBuildModeSyncSequences(t *testing.T) {
	tests := []struct {
		name          string
		alternateOn   bool
		appCursorKeys bool
		cursorVisible bool
		want          string
	}{
		{
			name:          "normal screen, app cursor keys on, cursor visible",
			alternateOn:   false,
			appCursorKeys: true,
			cursorVisible: true,
			want:          "\x1b[?1h\x1b[?25h",
		},
		{
			name:          "normal screen, app cursor keys off, cursor hidden",
			alternateOn:   false,
			appCursorKeys: false,
			cursorVisible: false,
			want:          "\x1b[?1l\x1b[?25l",
		},
		{
			name:          "alt-screen pane: no preamble regardless of other flags",
			alternateOn:   true,
			appCursorKeys: true,
			cursorVisible: false,
			want:          "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildModeSyncSequences(tt.alternateOn, tt.appCursorKeys, tt.cursorVisible)
			if got != tt.want {
				t.Errorf("buildModeSyncSequences(%v, %v, %v) = %q, want %q",
					tt.alternateOn, tt.appCursorKeys, tt.cursorVisible, got, tt.want)
			}
		})
	}
}
