package shellruntime

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestCustodyCleanupAuthorityCannotTransfer(t *testing.T) {
	authority, err := newCleanupAuthority("custody", "helper")
	if err != nil {
		t.Fatal(err)
	}
	request := authority.request("transaction")
	if !authority.verify(request) {
		t.Fatal("valid local close refused")
	}
	for _, field := range []string{"custody", "helper", "transaction", "mac"} {
		t.Run(field, func(t *testing.T) {
			stale := request
			switch field {
			case "custody":
				stale.Custody = "other"
			case "helper":
				stale.Helper = "other"
			case "transaction":
				stale.Transaction = "other"
			case "mac":
				stale.MAC = "forged"
			}
			if authority.verify(stale) {
				t.Fatal("forged cleanup accepted")
			}
		})
	}
	other, err := newCleanupAuthority("custody", "helper")
	if err != nil {
		t.Fatal(err)
	}
	if other.verify(request) {
		t.Fatal("old cleanup key closed a new helper")
	}
	if authority.verify(authority.request("")) {
		t.Fatal("empty transaction accepted")
	}
}

func TestCustodyCleanupKeyIsPrivateAndRedacted(t *testing.T) {
	authority, err := newCleanupAuthority("custody", "helper")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(authority)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "{}" {
		t.Fatal("cleanup key serialized outside bootstrap")
	}
	printed := fmt.Sprintf("%v %+v %#v", authority, authority, authority)
	if printed != "<shell cleanup authority> <shell cleanup authority> <shell cleanup authority>" {
		t.Fatal("cleanup key printed")
	}
	copied := *authority
	if fmt.Sprintf("%v %+v %#v", copied, copied, copied) != "<shell cleanup authority> <shell cleanup authority> <shell cleanup authority>" {
		t.Fatal("copied cleanup key printed")
	}
	raw, err = json.Marshal(struct {
		Authority cleanupAuthority `json:"authority"`
	}{Authority: copied})
	if err != nil || string(raw) != `{"authority":{}}` {
		t.Fatal("copied cleanup authority serialized")
	}
	// A transported bootstrap key may construct only this exact local authority.
	cloned, err := cleanupAuthorityFromBootstrap("custody", "helper", authority.bootstrapKey())
	if err != nil {
		t.Fatal(err)
	}
	if !cloned.verify(authority.request("transaction")) {
		t.Fatal("private bootstrap lost authority")
	}
	if _, err = cleanupAuthorityFromBootstrap("custody", "helper", []byte("short")); err == nil {
		t.Fatal("short cleanup key accepted")
	}
}
