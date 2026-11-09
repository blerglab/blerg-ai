//go:build linux

package shellkeeper

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

func TestMutualProofBindsEveryFieldAndRole(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	proof := transcript{Version: Version, ClientNonce: "client", ServerNonce: "server", Generation: "keeper", Epoch: Epoch{Number: 1, ID: "epoch"}}
	signature := sign(key, "keeper", proof)
	if !verify(pub, "keeper", proof, signature) || verify(pub, "daemon", proof, signature) {
		t.Fatal("role separation")
	}
	for name, mutate := range map[string]func(*transcript){
		"operation":    func(p *transcript) { p.Operation = "drain" },
		"version":      func(p *transcript) { p.Version++ },
		"client_nonce": func(p *transcript) { p.ClientNonce = "other" },
		"server_nonce": func(p *transcript) { p.ServerNonce = "other" },
		"generation":   func(p *transcript) { p.Generation = "other" },
		"epoch_number": func(p *transcript) { p.Epoch.Number++ },
		"epoch_id":     func(p *transcript) { p.Epoch.ID = "other" },
		"client_key":   func(p *transcript) { p.ClientPublic = []byte("other") },
		"keeper_key":   func(p *transcript) { p.KeeperPublic = []byte("other") },
		"client_build": func(p *transcript) { p.ClientBuild = "other" },
		"keeper_build": func(p *transcript) { p.KeeperBuild = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := proof
			mutate(&changed)
			if verify(pub, "keeper", changed, signature) {
				t.Fatal("altered proof accepted")
			}
		})
	}
}

func TestEpochRefusesRollbackAndEqualNumberFork(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	e := Epoch{Number: 2, ID: strings.Repeat("a", 64)}
	e.Signature = sign(key, "epoch", epochBody(e))
	if !validEpoch(pub, e) {
		t.Fatal("signed epoch refused")
	}
	for _, other := range []Epoch{{Number: 1, ID: "prior"}, {Number: 2, ID: "fork"}, {Number: 0, ID: "zero"}} {
		other.Signature = sign(key, "epoch", epochBody(other))
		if follows(e, other) {
			t.Fatalf("epoch admitted: %d", other.Number)
		}
	}
	if !follows(e, e) || !follows(e, Epoch{Number: 3, ID: "next"}) {
		t.Fatal("continuation refused")
	}
}
