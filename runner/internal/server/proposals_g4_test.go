package server

// Round-2 fix G4: the proposal JSON carries the frozen arguments as the stored jsonb text, so the
// browser can show exactly the lexemes the gateway sends.

import (
	"encoding/json"
	"testing"
)

func TestProposalViewCarriesArgumentsRawAsStored(t *testing.T) {
	fx := newPropFx(t, 0)
	p := fx.insert("send", `{"big": 12345678901234567890, "price": 1.10, "a": "x"}`)
	want := string(fx.row(p.ID).Arguments) // what the gateway receives on approval

	rec := fx.req("GET", "/api/proposals/"+p.ID, fx.human(), "")
	if rec.Code != 200 {
		t.Fatalf("get = %d %s", rec.Code, rec.Body.String())
	}
	var v struct {
		Raw  *string         `json:"arguments_raw"`
		Args json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Raw == nil || *v.Raw != want {
		t.Fatalf("arguments_raw = %v, want the stored text %q", v.Raw, want)
	}
	if len(v.Args) == 0 {
		t.Fatal("arguments must stay for compatibility")
	}
	list, code := fx.listProposals("")
	if code != 200 || len(list.Proposals) != 1 {
		t.Fatalf("list = %d %d", code, len(list.Proposals))
	}
}
