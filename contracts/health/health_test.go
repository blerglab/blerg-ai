package health

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReadiness(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func(context.Context) error
		code  int
	}{
		{"starting", nil, 503},
		{"unavailable", func(context.Context) error { return errors.New("private database detail") }, 503},
		{"ready", func(ctx context.Context) error {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 2*time.Second {
				t.Error("probe has no bounded deadline")
			}
			return nil
		}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			Handler(tc.check)(rec, httptest.NewRequest("GET", "/readyz", nil))
			if rec.Code != tc.code {
				t.Fatalf("status=%d", rec.Code)
			}
			if tc.code == 503 && rec.Body.String() != "not ready\n" {
				t.Fatal("probe leaked database error")
			}
		})
	}
}
