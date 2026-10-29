// Package idempotency defines the runner's retry-key contract and scope encoding.
package idempotency

import (
	"regexp"
	"strconv"
	"strings"
)

const (
	MinLength = 1
	MaxLength = 128
	Pattern   = `^[A-Za-z0-9_.:-]{1,128}$`
	Message   = "idempotency key must be a single value of 1-128 characters using only A-Z, a-z, 0-9, '_', '.', ':' or '-'"
)

var keyPattern = regexp.MustCompile(Pattern)

// Valid checks a supplied key. Omission is handled by the transport; an empty
// supplied value is invalid. ASCII makes byte and character bounds identical.
func Valid(key string) bool { return keyPattern.MatchString(key) }

// Scope encodes every part, including the namespace, using its byte length.
// Colons, empty parts, percent signs and arbitrary UTF-8 cannot move a boundary.
// The version prefix separates these scopes from all legacy runner namespaces.
// Do not fall back to ambiguous legacy scopes: their parts could name another caller.
func Scope(namespace string, parts ...string) string {
	var b strings.Builder
	b.WriteString("v2|")
	writePart := func(part string) {
		b.WriteString(strconv.Itoa(len(part)))
		b.WriteByte(':')
		b.WriteString(part)
	}
	writePart(namespace)
	for _, part := range parts {
		writePart(part)
	}
	return b.String()
}
