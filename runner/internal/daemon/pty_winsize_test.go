package daemon

import "testing"

func TestWinsizeClamps(t *testing.T) {
	for _, c := range []struct {
		in   int
		want uint16
	}{{-5, 0}, {0, 0}, {80, 80}, {65535, 65535}, {65616, 65535}, {1 << 40, 65535}} {
		w := winsize(c.in, c.in)
		if w.Cols != c.want || w.Rows != c.want {
			t.Errorf("winsize(%d) = %d/%d, want %d", c.in, w.Cols, w.Rows, c.want)
		}
	}
}
