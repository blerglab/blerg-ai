package health

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"unicode"
)

// maxReason caps a reason's length so one runaway error cannot flood a log line or /readyz.
const maxReason = 300

const redacted = "[redacted]"

var (
	// scheme://user:password@host: drop the whole userinfo.
	urlUserinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.\-]*://)[^\s/@'"]*@`)
	// key=value connection strings and query parameters that carry a password.
	passwordParam = regexp.MustCompile(`(?i)\b(password|passwd|pwd|sslpassword|sslkey|sslcert)\s*=\s*('[^']*'|"[^"]*"|[^\s&;]+)`)
)

// SafeReason says why a database step failed in words safe to log and to show on /readyz: an
// error class (a SQLSTATE, "timeout", "network", the Go type, or "error" for a plain one) and the error's message, with
// every literal secret given, any URL userinfo and any password parameter removed, on one line
// and capped in length.
func SafeReason(err error, secrets ...string) string {
	if err == nil {
		return ""
	}
	return errorClass(err) + ": " + scrub(err.Error(), secrets)
}

func errorClass(err error) string {
	var state interface{ SQLState() string }
	if errors.As(err, &state) && state.SQLState() != "" {
		return "SQLSTATE " + state.SQLState()
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "timeout"
		}
		return "network"
	}
	root := err
	for next := errors.Unwrap(root); next != nil; next = errors.Unwrap(root) {
		root = next
	}
	name := fmt.Sprintf("%T", root)
	if strings.HasPrefix(name, "*errors.") || strings.HasPrefix(name, "*fmt.") {
		return "error" // a plain error says nothing more by its type
	}
	return name
}

func scrub(text string, secrets []string) string {
	ordered := append([]string(nil), secrets...)
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, secret := range ordered {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, redacted)
		}
	}
	text = urlUserinfo.ReplaceAllString(text, "${1}"+redacted+"@")
	text = passwordParam.ReplaceAllString(text, "${1}="+redacted)
	text = strings.Join(strings.FieldsFunc(text, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if runes := []rune(text); len(runes) > maxReason {
		text = string(runes[:maxReason-1]) + "…"
	}
	return text
}

// sqlStateNames are the SQLSTATEs a startup most often meets, named for /readyz.
var sqlStateNames = map[string]string{
	"28000": "authentication failed",
	"28P01": "authentication failed",
	"3D000": "database does not exist",
	"42501": "permission denied",
	"53300": "too many connections",
	"57P03": "database is starting up",
	"55P03": "lock not available",
}

// ReadinessClass says what kind of failure err is, in fixed words safe for a public /readyz:
// "database: SQLSTATE 28P01 (authentication failed)", "database: connection refused". Unlike
// SafeReason it never carries the error's text, so no user, host, port or database name.
func ReadinessClass(err error) string {
	if err == nil {
		return ""
	}
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		if code := state.SQLState(); sqlStateCode.MatchString(code) {
			if name := sqlStateNames[code]; name != "" {
				return "database: SQLSTATE " + code + " (" + name + ")"
			}
			return "database: SQLSTATE " + code
		}
	}
	var dnsErr *net.DNSError
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "database: connection refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "database: connection reset"
	case errors.As(err, &dnsErr):
		return "database: host name not resolved"
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		return "database: timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "database: timeout"
		}
		return "database: network error"
	}
	return "database: error"
}

var sqlStateCode = regexp.MustCompile(`^[0-9A-Z]{5}$`)
