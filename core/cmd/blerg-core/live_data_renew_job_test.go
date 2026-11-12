package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/livetls"
)

func TestLiveDataRenewJobScopeCASAndNoAuthorityReplacement(t *testing.T) {
	for _, mode := range []string{"renew", "wrong identity", "wrong CA", "missing leaf", "conflict", "partial conflict", "redirect", "missing authority"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			now := time.Now()
			if e := livetls.ProvisionAt(root, now.Add(-100*24*time.Hour)); e != nil {
				t.Fatal(e)
			}
			records := map[string]map[string]any{}
			oldSerial := map[string]string{}
			for _, role := range []string{"core", "runner"} {
				data := map[string][]byte{}
				for _, name := range []string{"identity.json", "key.pem", "cert.pem", "ca.pem"} {
					raw, e := os.ReadFile(filepath.Join(root, role, name))
					if e != nil {
						t.Fatal(e)
					}
					data[name] = raw
				}
				pair, e := tls.X509KeyPair(data["cert.pem"], data["key.pem"])
				if e != nil {
					t.Fatal(e)
				}
				cert, e := x509.ParseCertificate(pair.Certificate[0])
				if e != nil {
					t.Fatal(e)
				}
				oldSerial[role] = cert.SerialNumber.String()
				if role == "runner" && mode == "wrong identity" {
					data["identity.json"] = []byte(`{"installation":"another","runner":"another","role":"runner"}`)
				}
				if role == "runner" && mode == "wrong CA" {
					data["ca.pem"] = []byte("foreign CA")
				}
				records["blerg-live-data-"+role] = map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "blerg-live-data-" + role, "namespace": "blerg", "resourceVersion": "7", "annotations": map[string]string{"preserved": "yes"}}, "data": data}
			}
			var calls, puts atomic.Int64
			var conflicted atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer fixture-job-token" {
					t.Error("missing dedicated Job credential")
					w.WriteHeader(403)
					return
				}
				name := strings.TrimPrefix(r.URL.Path, "/secrets/")
				record, ok := records[name]
				if !ok {
					t.Error("request outside two leaf Secrets")
					w.WriteHeader(403)
					return
				}
				if mode == "redirect" {
					w.Header().Set("Location", "/secrets/blerg-live-data-core")
					w.WriteHeader(307)
					return
				}
				if mode == "missing leaf" && strings.HasSuffix(name, "runner") {
					w.WriteHeader(404)
					return
				}
				if r.Method == "PUT" {
					if mode == "conflict" || mode == "partial conflict" && strings.HasSuffix(name, "runner") && !conflicted.Swap(true) {
						w.WriteHeader(409)
						return
					}
					var body map[string]any
					if json.NewDecoder(r.Body).Decode(&body) != nil {
						t.Error("invalid PUT")
						w.WriteHeader(400)
						return
					}
					meta, ok := body["metadata"].(map[string]any)
					if !ok || meta["resourceVersion"] != "7" {
						t.Error("CAS version lost")
						w.WriteHeader(409)
						return
					}
					annotations, ok := meta["annotations"].(map[string]any)
					if !ok || annotations["preserved"] != "yes" {
						t.Error("metadata lost")
						w.WriteHeader(400)
						return
					}
					record = body
					records[name] = body
					puts.Add(1)
				} else if r.Method != "GET" {
					t.Error("unexpected API authority")
					w.WriteHeader(403)
					return
				}
				_ = json.NewEncoder(w).Encode(record)
			}))
			defer server.Close()
			authority := filepath.Join(root, "authority", "bundle")
			if mode == "missing authority" {
				authority = t.TempDir()
			}
			job := liveLeafJob{Client: server.Client(), BaseURL: server.URL + "/secrets/", Authority: authority, Token: func() (string, error) { return "fixture-job-token", nil }}
			e := job.run(context.Background(), now)
			if mode == "partial conflict" {
				if e == nil || puts.Load() != 1 {
					t.Fatal("partial conflict not fenced", e, puts.Load())
				}
				calls.Store(0)
				puts.Store(0)
				e = job.run(context.Background(), now)
			} else if mode != "renew" {
				if e == nil || puts.Load() != 0 {
					t.Fatal("refusal mutated leaf state", mode, e, puts.Load())
				}
				if mode == "redirect" && calls.Load() != 1 {
					t.Fatal("followed redirect")
				}
				return
			}
			if e != nil || calls.Load() != 4 || puts.Load() != 2 {
				t.Fatal("renewal failed", e, calls.Load(), puts.Load())
			}
			for _, role := range []string{"core", "runner"} {
				raw, err := json.Marshal(records["blerg-live-data-"+role]["data"])
				if err != nil {
					t.Fatal(err)
				}
				var data map[string][]byte
				if json.Unmarshal(raw, &data) != nil {
					t.Fatal("invalid Secret data")
				}
				out := t.TempDir()
				for name, value := range data {
					if os.WriteFile(filepath.Join(out, name), value, 0600) != nil {
						t.Fatal("write renewed fixture")
					}
				}
				identity, cause := livetls.Available(out, role, now)
				if identity == nil {
					t.Fatal("renewed leaf invalid", cause)
				}
				pair, err := tls.X509KeyPair(data["cert.pem"], data["key.pem"])
				if err != nil {
					t.Fatal(err)
				}
				cert, err := x509.ParseCertificate(pair.Certificate[0])
				if err != nil {
					t.Fatal(err)
				}
				if cert.SerialNumber.String() == oldSerial[role] {
					t.Fatal("leaf was not renewed")
				}
			}
		})
	}
}
