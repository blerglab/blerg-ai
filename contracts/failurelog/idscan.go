package failurelog

import "encoding/json"

// idScanner reads only top-level error_id strings. Other string values are
// skipped byte by byte, so even a large error response cannot retain its content.
// Its fixed buffer accommodates a UUID with every byte JSON-escaped.
type idScanner struct {
	depth                                           int
	started, done, invalid                          bool
	quoted, escaped, key, value, wantKey, wantValue bool
	overflow                                        bool
	buf                                             [256]byte
	n                                               int
	id                                              string
}

func (s *idScanner) write(p []byte) {
	for _, c := range p {
		if s.invalid {
			return
		}
		if s.done {
			if c != ' ' && c != '\n' && c != '\r' && c != '\t' {
				s.invalid = true
			}
			continue
		}
		if !s.started {
			if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
				continue
			}
			if c != '{' {
				s.invalid = true
				return
			}
			s.started = true
		}
		if s.quoted {
			if s.key || s.value {
				if s.n < len(s.buf) {
					s.buf[s.n] = c
					s.n++
				} else {
					s.overflow = true
				}
			}
			if s.escaped {
				s.escaped = false
				continue
			}
			if c == '\\' {
				s.escaped = true
				continue
			}
			if c == '"' {
				s.quoted = false
				if !s.overflow && (s.key || s.value) {
					var text string
					if json.Unmarshal(s.buf[:s.n], &text) == nil {
						if s.key {
							s.wantValue = text == "error_id"
						}
						if s.value {
							s.id = ""
							if safeID(text) {
								s.id = text
							}
						}
					}
				}
				if s.key {
					s.wantKey = false
				}
			}
			continue
		}
		switch c {
		case '"':
			s.quoted = true
			s.escaped = false
			s.key = s.depth == 1 && s.wantKey
			s.value = s.depth == 1 && !s.wantKey && s.wantValue
			s.n = 1
			s.buf[0] = '"'
			s.overflow = false
		case '{', '[':
			s.depth++
			if s.depth == 1 {
				s.wantKey = true
			}
		case '}', ']':
			s.depth--
			if s.depth == 0 {
				s.done = true
			}
			if s.depth < 0 {
				s.invalid = true
			}
		case ',':
			if s.depth == 1 {
				s.wantKey = true
				s.wantValue = false
			}
		case ':':
			// The next top-level string is the value of the key just read.
		default:
			if s.depth == 1 && c != ' ' && c != '\n' && c != '\r' && c != '\t' {
				// A non-string value is not an error ID.
				if s.wantValue {
					s.id = ""
					s.wantValue = false
				}
			}
		}
	}
}

func (s *idScanner) errorID() string {
	if !s.done || s.invalid {
		return ""
	}
	return s.id
}
