package daemon

import (
	"bytes"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"log"
	"strings"
	"testing"
)

func TestCodexConnectionDiagnosticRedactsBeforeRetention(t *testing.T) {
	d := &codexDriver{env: []string{"OPENAI_API_KEY=fixture-env-secret"}, managedCredential: codexCredentialState{secrets: []string{"fixture-managed-secret"}}}
	text := d.safeCodexError(codexTurnError{Message: "stream failed", AdditionalDetails: `TLS handshake failed; Bearer fixture-bearer; access_token="fixture-access" refresh_token:fixture-refresh fixture-env-secret fixture-managed-secret https://user:fixture-password@example.test/responses?key=fixture-query#fixture-fragment`, CodexErrorInfo: json.RawMessage(`{"responseStreamConnectionFailed":{"httpStatusCode":401}}`)})
	for _, secret := range []string{"fixture-env-secret", "fixture-managed-secret", "fixture-bearer", "fixture-access", "fixture-refresh", "fixture-password", "fixture-query", "fixture-fragment"} {
		if strings.Contains(text, secret) {
			t.Fatal("engine credential leaked in diagnostic")
		}
	}
	for _, detail := range []string{"TLS handshake failed", "responseStreamConnectionFailed", "HTTP 401", "https://example.test/responses"} {
		if !strings.Contains(text, detail) {
			t.Fatal("safe engine cause was lost")
		}
	}
	if len(d.safeCodexError(codexTurnError{Message: strings.Repeat("failure ", 10000)})) > 2051 {
		t.Fatal("unbounded diagnostic")
	}
}

func TestCodexSnapshotCredentialsRedactedFromCausesAndLogs(t *testing.T) {
	for _, field := range []string{"OPENAI_API_KEY", "access_token", "refresh_token", "id_token"} {
		t.Run(field, func(t *testing.T) {
			secret := "short-private-" + field
			auth := map[string]any{"tokens": map[string]string{field: secret}}
			if field == "OPENAI_API_KEY" {
				auth = map[string]any{field: secret}
			}
			bundle, _ := json.Marshal(auth)
			em := &collectEmitter{}
			d := &codexDriver{env: []string{"CODEX_AUTH_JSON=" + string(bundle)}, emitter: em}
			var logs bytes.Buffer
			before := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(before)
			for _, diagnostic := range []string{"Incorrect API key provided: " + secret, "Provider rejected " + secret, field + "=\"" + secret + "\""} {
				cause := d.safeCodexError(codexTurnError{Message: diagnostic})
				if strings.Contains(cause, secret) {
					t.Error("credential retained in cause")
				}
				d.emitCodexError(cause, false)
			}
			for _, ev := range em.snapshot() {
				if p, ok := ev.Payload.(agent.ErrorPayload); ok && strings.Contains(p.Message, secret) {
					t.Error("credential retained in event")
				}
			}
			if strings.Contains(logs.String(), secret) {
				t.Error("credential retained in logs")
			}
		})
	}
}

func TestCodexTokenShapesRedactedFromCausesAndLogs(t *testing.T) {
	for _, value := range []string{"eyJmaXh0dXJlIjoxfQ.eyJleHAiOjF9.c2ln", "aGVhZGVy.Y2xhaW1z.c2lnbmF0dXJl", "header.payload.signature", "eyJ" + strings.Repeat("a", 48) + ".eyJleHAiOjF9.c2ln", strings.Repeat("a", 48)} {
		em := &collectEmitter{}
		d := &codexDriver{emitter: em}
		var logs bytes.Buffer
		before := log.Writer()
		log.SetOutput(&logs)
		cause := d.safeCodexError(codexTurnError{Message: "Provider rejected " + value})
		d.emitCodexError(cause, false)
		log.SetOutput(before)
		if cause != "Provider rejected [redacted]" || strings.Contains(logs.String(), value) {
			t.Error("token-shaped credential retained")
		}
		for _, ev := range em.snapshot() {
			if p, ok := ev.Payload.(agent.ErrorPayload); ok && strings.Contains(p.Message, value) {
				t.Error("token-shaped credential retained in event")
			}
		}
	}
}

func TestCodexSnapshotEscapedCredentialRedacted(t *testing.T) {
	secret := "short-private-\"\\value"
	bundle, _ := json.Marshal(map[string]string{"OPENAI_API_KEY": secret})
	encoded, _ := json.Marshal(secret)
	cause := (&codexDriver{env: []string{"CODEX_AUTH_JSON=" + string(bundle)}}).safeCodexError(codexTurnError{Message: "Provider rejected " + secret + "; " + string(encoded)})
	if strings.Contains(cause, secret) || strings.Contains(cause, string(encoded[1:len(encoded)-1])) {
		t.Fatal("escaped credential retained")
	}
}
