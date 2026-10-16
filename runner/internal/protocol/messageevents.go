package protocol

import (
	"crypto/sha1" //nolint:gosec // UUID derivation, not a security use
	"fmt"
)

// messageEventNamespace scopes messageEventID (an arbitrary, fixed UUID).
var messageEventNamespace = [16]byte{0x6b, 0x1e, 0x3a, 0x52, 0x0f, 0x8d, 0x4c, 0x2b, 0x9a, 0x77, 0x41, 0x05, 0xd3, 0xe8, 0x60, 0x19}

// SessionMessageEventID is the client_event_id of the transcript event recorded for a message: role "ask",
// "update" or "note" for the message itself, "answer" for an ask's answer. A UUID v5 of the two.
func SessionMessageEventID(messageID, role string) string {
	h := sha1.New() //nolint:gosec // see the import
	h.Write(messageEventNamespace[:])
	h.Write([]byte(messageID + "\x00" + role))
	b := h.Sum(nil)[:16]
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
