// Package theme defines the account theme vocabulary and a bounded same-origin proxy.
package theme

import (
	"io"
	"net/http"
	"strings"
	"time"
)

// Valid reports whether value names a supported account appearance preference. The five after
// high-contrast are bundled open-source schemes (BLG-474, packages/settings/theme.ts).
func Valid(value string) bool {
	switch value {
	case "system", "dark", "light", "colorblind-dark", "colorblind-light", "high-contrast",
		"catppuccin-mocha", "catppuccin-latte", "dracula", "nord", "tokyo-night":
		return true
	}
	return false
}

// Proxy forwards only this preference, using the original human bearer as proof.
// Core verifies the fixed app audience, account and live browser session itself.
func Proxy(coreURL, audience string) http.HandlerFunc {
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if coreURL == "" {
			http.Error(w, "theme sync unavailable", http.StatusServiceUnavailable)
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, strings.TrimRight(coreURL, "/")+"/api/me/theme", http.MaxBytesReader(w, r.Body, 1024))
		if err != nil {
			http.Error(w, "theme sync unavailable", http.StatusServiceUnavailable)
			return
		}
		req.Header.Set("Authorization", r.Header.Get("Authorization"))
		req.Header.Set("X-Blerg-Theme-Audience", audience)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req) // #nosec G704 -- Fixed path on operator-configured Core URL, redirects disabled.
		if err != nil {
			http.Error(w, "theme sync unavailable", http.StatusServiceUnavailable)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			http.Error(w, "theme sync unavailable", http.StatusServiceUnavailable)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 2049))
		if err != nil || len(raw) > 2048 {
			http.Error(w, "theme sync unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(raw)
	}
}
