// Package health provides bounded readiness probes shared by the services.
package health

import (
	"context"
	"net/http"
	"time"
)

// Handler returns ready only after the supplied database and migration check succeeds.
func Handler(check func(context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if check == nil || check(ctx) != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}
}
