package ownershell

// ScrollbackBytes is how much of each shell's most recent output the server
// keeps, attached or not, so a reattaching browser (a refresh, another tab of
// the same login, a return after a disconnect) is shown what it missed. It
// lives only in server memory, is never persisted, and is freed when the shell
// ends. Worst case: ScrollbackBytes × MaxShells per session × live sessions;
// the runner admits at most 1024 live shells in total (remoteShell), so at most
// 256 MiB server-wide, and an idle shell holds only what it has printed.
const ScrollbackBytes = 256 << 10

// scrollback is a byte ring holding the last ScrollbackBytes of one shell's
// output. end counts every byte ever written, so the ring holds stream offsets
// [end-len, end): the replay boundary is an exact offset, not a guess.
type scrollback struct {
	buf  []byte
	head int // write position once full
	end  uint64
}

func (r *scrollback) write(p []byte) {
	if r == nil {
		return
	}
	r.end += uint64(len(p))
	if len(p) >= ScrollbackBytes {
		p = p[len(p)-ScrollbackBytes:]
		r.buf = append(make([]byte, 0, ScrollbackBytes), p...)
		r.head = 0
		return
	}
	if len(r.buf) < ScrollbackBytes {
		need := len(r.buf) + len(p)
		if need > cap(r.buf) {
			grown := make([]byte, len(r.buf), min(ScrollbackBytes, max(need, 2*cap(r.buf))))
			copy(grown, r.buf)
			r.buf = grown
		}
		room := min(ScrollbackBytes-len(r.buf), len(p))
		r.buf = append(r.buf, p[:room]...)
		p = p[room:]
	}
	for len(p) > 0 {
		n := copy(r.buf[r.head:], p)
		p = p[n:]
		r.head = (r.head + n) % ScrollbackBytes
	}
}

// snapshot returns a copy of the kept bytes, oldest first, and the stream
// offset just past them.
func (r *scrollback) snapshot() ([]byte, uint64) {
	if r == nil {
		return nil, 0
	}
	out := make([]byte, 0, len(r.buf))
	out = append(out, r.buf[r.head:]...)
	out = append(out, r.buf[:r.head]...)
	return out, r.end
}
