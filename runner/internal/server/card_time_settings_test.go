package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func cardTimeBudgetRequest(api *API, method, tok, body string) *httptest.ResponseRecorder {
	var r *strings.Reader
	if body == "" {
		r = strings.NewReader("")
	} else {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/api/settings/card-time-budget", r)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	w := httptest.NewRecorder()
	api.HandleCardTimeBudgetSettings(w, req)
	return w
}

// Owner's decision 4, corrected: per-account setting (the signed-in person's own), not
// install-wide — following HandlePlanNudges' auth shape, not HandleUsageNoticeSettings'. Defaults
// to 4h (owner's decision 3) and round-trips a PUT.
func TestCardTimeBudgetSettingsDefaultAndRoundTrip(t *testing.T) {
	fx := newTopicFx(t)
	tok := fx.token(topicOwner, "human")

	w := cardTimeBudgetRequest(fx.api, http.MethodGet, tok, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", w.Code, w.Body.String())
	}
	var got struct {
		IntervalHours int `json:"interval_hours"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.IntervalHours != 4 {
		t.Fatalf("default: got %d, want 4", got.IntervalHours)
	}

	w = cardTimeBudgetRequest(fx.api, http.MethodPut, tok, `{"interval_hours":8}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}

	w = cardTimeBudgetRequest(fx.api, http.MethodGet, tok, "")
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.IntervalHours != 8 {
		t.Fatalf("after PUT: got %d, want 8", got.IntervalHours)
	}
}

func TestCardTimeBudgetSettingsRejectsOutOfRange(t *testing.T) {
	fx := newTopicFx(t)
	tok := fx.token(topicOwner, "human")
	for _, body := range []string{`{"interval_hours":0}`, `{"interval_hours":169}`, `{"interval_hours":-1}`} {
		w := cardTimeBudgetRequest(fx.api, http.MethodPut, tok, body)
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: got %d, want 422", body, w.Code)
		}
	}
}

func TestCardTimeBudgetSettingsRequiresSignIn(t *testing.T) {
	fx := newTopicFx(t)
	w := cardTimeBudgetRequest(fx.api, http.MethodGet, "", "")
	if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Fatalf("no token: got %d, want 401 or 403", w.Code)
	}
}

// M4: account A's own setting is independent of account B's — changing one must never affect, or
// be visible in, the other's.
func TestCardTimeBudgetSettingsIsPerAccount(t *testing.T) {
	fx := newTopicFx(t)
	tokA := fx.token(topicOwner, "human")
	tokB := fx.token(topicOther, "human")

	w := cardTimeBudgetRequest(fx.api, http.MethodPut, tokA, `{"interval_hours":12}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT for account A: %d %s", w.Code, w.Body.String())
	}

	w = cardTimeBudgetRequest(fx.api, http.MethodGet, tokB, "")
	var got struct {
		IntervalHours int `json:"interval_hours"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.IntervalHours != 4 {
		t.Fatalf("account B after account A's PUT: got %d, want the untouched default 4", got.IntervalHours)
	}

	w = cardTimeBudgetRequest(fx.api, http.MethodGet, tokA, "")
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.IntervalHours != 12 {
		t.Fatalf("account A: got %d, want its own 12 (unaffected by reading account B's)", got.IntervalHours)
	}
}
