package testfixture

import (
	"strings"
	"testing"
	"time"
)

func TestEligibleRequiresExactMarkedIdentityScopeOwnerAndAge(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	scope := Scope{Database: "cx_hygiene_admin", DatabaseOID: 10, Role: "fixture-owner", RoleOID: 20}
	meta := Metadata{Version: 1, Module: "runner", Database: "cx_runner_" + strings.Repeat("a", 24), DatabaseOID: 30, RunID: strings.Repeat("b", 24), CreatedAt: now.Add(-2 * time.Hour), Scope: scope}
	comment, err := Encode(meta)
	if err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{Database: meta.Database, DatabaseOID: 30, OwnerOID: 20, Comment: comment}
	if _, ok := Eligible(candidate, scope, "runner", now, time.Hour); !ok {
		t.Fatal("valid abandoned fixture rejected")
	}
	for _, change := range []func(*Candidate){func(c *Candidate) { c.Comment = "" }, func(c *Candidate) { c.Database = "cx_runner_legacy" }, func(c *Candidate) { c.DatabaseOID++ }, func(c *Candidate) { c.OwnerOID++ }, func(c *Candidate) { c.Template = true }, func(c *Candidate) { c.Comment += " " }} {
		bad := candidate
		change(&bad)
		if _, ok := Eligible(bad, scope, "runner", now, time.Hour); ok {
			t.Fatalf("unsafe candidate accepted %+v", bad)
		}
	}
	if _, ok := Eligible(candidate, scope, "board", now, time.Hour); ok {
		t.Fatal("wrong module accepted")
	}
	other := scope
	other.DatabaseOID++
	if _, ok := Eligible(candidate, other, "runner", now, time.Hour); ok {
		t.Fatal("wrong admin scope accepted")
	}
	if _, ok := Eligible(candidate, scope, "runner", now, 3*time.Hour); ok {
		t.Fatal("young fixture accepted")
	}
	if _, ok := Eligible(candidate, scope, "runner", now.Add(-3*time.Hour), time.Hour); ok {
		t.Fatal("future fixture accepted")
	}
	if _, ok := Eligible(candidate, scope, "runner", now, 0); ok {
		t.Fatal("invalid age accepted")
	}
}
