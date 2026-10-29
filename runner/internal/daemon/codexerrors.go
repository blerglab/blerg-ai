package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
)

// These are untrusted provider diagnostics, never process stderr. Redact before
// retaining, emitting or logging them; the same bounded text reaches both sinks.
type codexTurnError struct {
	Message           string          `json:"message"`
	AdditionalDetails string          `json:"additionalDetails"`
	CodexErrorInfo    json.RawMessage `json:"codexErrorInfo"`
}

var codexDiagnosticURL = regexp.MustCompile(`https?://[^\s<>"']+`)
var codexDiagnosticAuth = regexp.MustCompile(`(?i)(\b(?:bearer|basic)\s+)[^\s,;"']+`)
var codexDiagnosticField = regexp.MustCompile(`(?i)((?:["']?(?:access[_-]?token|refresh[_-]?token|id[_-]?token|api[_-]?key|password|secret|token)["']?)\s*[:=]\s*["']?)[^\s,;"'}]+`)
var codexDiagnosticOpaque = regexp.MustCompile(`(?:eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*|[A-Za-z0-9_-]{3,}\.[A-Za-z0-9_-]{3,}\.[A-Za-z0-9_-]{3,}|[A-Za-z0-9_+/=-]{40,})`)

func (d *codexDriver) safeCodexError(e codexTurnError) string {
	text := e.Message
	if e.AdditionalDetails != "" {
		text += ": " + e.AdditionalDetails
	}
	// Preserve native connection classes and HTTP status without reflecting
	// unknown provider fields into either the transcript or log.
	var info map[string]struct {
		HTTPStatusCode *int `json:"httpStatusCode"`
	}
	if json.Unmarshal(e.CodexErrorInfo, &info) == nil {
		for _, kind := range []string{"httpConnectionFailed", "responseStreamConnectionFailed", "responseStreamDisconnected", "responseTooManyFailedAttempts"} {
			if detail, ok := info[kind]; ok {
				text += " [" + kind
				if detail.HTTPStatusCode != nil && *detail.HTTPStatusCode >= 100 && *detail.HTTPStatusCode <= 599 {
					text += fmt.Sprintf(", HTTP %d", *detail.HTTPStatusCode)
				}
				text += "]"
			}
		}
	}
	// Explicit secrets cover arbitrary short values in the snapshot container,
	// as well as ordinary credential environment variables.
	var secrets []string
	for _, item := range d.env {
		key, value, ok := strings.Cut(item, "=")
		if !ok || value == "" {
			continue
		}
		if strings.EqualFold(key, "CODEX_AUTH_JSON") {
			secrets = append(secrets, value)
			var auth any
			if json.Unmarshal([]byte(value), &auth) == nil {
				secrets = append(secrets, codexJSONSecrets(auth)...)
			}
		} else if codexSecretField(key) {
			secrets = append(secrets, value)
		}
	}
	// Replace longer values first when one credential is a substring of another.
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, value := range secrets {
		text = strings.ReplaceAll(text, value, "[redacted]")
		encoded, _ := json.Marshal(value)
		if len(encoded) > 2 {
			text = strings.ReplaceAll(text, string(encoded[1:len(encoded)-1]), "[redacted]")
		}
	}
	d.managedCredential.mu.Lock()
	for _, value := range d.managedCredential.secrets {
		text = strings.ReplaceAll(text, value, "[redacted]")
	}
	d.managedCredential.mu.Unlock()
	text = codexDiagnosticURL.ReplaceAllStringFunc(text, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return "[redacted URL]"
		}
		u.User, u.RawQuery, u.Fragment = nil, "", ""
		return u.String()
	})
	text = codexDiagnosticAuth.ReplaceAllString(text, "${1}[redacted]")
	text = codexDiagnosticField.ReplaceAllString(text, "${1}[redacted]")
	text = codexDiagnosticOpaque.ReplaceAllString(text, "[redacted]")
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 2048 {
		text = text[:2048] + "…"
	}
	return text
}

func (d *codexDriver) emitCodexError(message string, retry bool) {
	if message == "" {
		return
	}
	code := agent.ErrorCodeAPI
	if retry {
		code = agent.ErrorCodeAPIRetry
	}
	log.Printf("codex: %s", message)
	d.emit("error", agent.ErrorPayload{Message: message, Retryable: true, Code: code})
}

func codexSecretField(key string) bool {
	key = strings.ToLower(key)
	return strings.Contains(key, "token") || strings.Contains(key, "key") || strings.Contains(key, "secret") || strings.Contains(key, "password")
}

func codexJSONSecrets(value any) []string {
	var secrets []string
	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			if scalar, ok := item.(string); ok && scalar != "" && codexSecretField(key) {
				secrets = append(secrets, scalar)
			}
			secrets = append(secrets, codexJSONSecrets(item)...)
		}
	case []any:
		for _, item := range value {
			secrets = append(secrets, codexJSONSecrets(item)...)
		}
	}
	return secrets
}
