//go:build linux

package shellkeeper

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
)

// Version is exact in both directions. A new executable cannot launch helpers
// through a keeper with an incompatible custody protocol.
const Version = 3

// Frozen maintenance frame, independent of the launch/adoption protocol.
const maintenanceVersion = 2

const maxCandidates = 512

var ErrRefused = errors.New("shell keeper refused identity or custody proof")
var ErrUnsupported = errors.New("host shells unavailable: kernel containment support is unavailable")

// Epoch is issued and durably stored by the daemon. Keeper generations are
// random instance identifiers, never substitutes for this signed authority.
type Epoch struct {
	Number    uint64 `json:"number"`
	ID        string `json:"id"`
	Signature []byte `json:"signature"`
}

type epochClaims struct {
	Number uint64 `json:"number"`
	ID     string `json:"id"`
}

func epochBody(e Epoch) epochClaims { return epochClaims{Number: e.Number, ID: e.ID} }

func validEpoch(key ed25519.PublicKey, e Epoch) bool {
	return e.Number > 0 && len(e.ID) == 64 && verify(key, "epoch", epochBody(e), e.Signature)
}

func publicKey(key ed25519.PrivateKey) ed25519.PublicKey {
	if len(key) != ed25519.PrivateKeySize {
		return nil
	}
	return ed25519.PublicKey(key[ed25519.SeedSize:])
}

func follows(prior, next Epoch) bool {
	return (prior.Number != ^uint64(0) && next.Number == prior.Number+1) || (next.Number == prior.Number && next.Number > 0 && next.ID == prior.ID)
}

type transcript struct {
	Operation    string `json:"operation,omitempty"`
	ClientPublic []byte `json:"client_public"`
	KeeperPublic []byte `json:"keeper_public"`
	ClientBuild  string `json:"client_build"`
	KeeperBuild  string `json:"keeper_build"`
	Version      int    `json:"version"`
	ClientNonce  string `json:"client_nonce"`
	ServerNonce  string `json:"server_nonce"`
	Generation   string `json:"generation"`
	Epoch        Epoch  `json:"epoch"`
}

// Candidate contains identifiers only. Inventory never authorizes I/O, replay,
// collection or a new writer. Those need an independent adoption transaction.
type Candidate struct {
	Allocation string `json:"allocation"`
	Custody    string `json:"custody"`
	Shell      string `json:"shell"`
}

// Empty launch registrations are lifecycle metadata, never proof of a live
// shell. They remain discoverable for retirement after a daemon restart.
func CustodyCandidates(entries []Candidate) []Candidate {
	var live []Candidate
	for _, entry := range entries {
		if entry != EmptyLaunchCandidate(entry.Allocation) {
			live = append(live, entry)
		}
	}
	return live
}
func EmptyLaunchCandidate(allocation string) Candidate {
	digest := sha256.Sum256([]byte(allocation))
	return Candidate{Allocation: allocation, Custody: "launch", Shell: "launch:" + hex.EncodeToString(digest[:])}
}

type inventory struct {
	Proof      transcript  `json:"proof"`
	Candidates []Candidate `json:"candidates"`
}

type message struct {
	MAC        []byte      `json:"mac,omitempty"`
	Proof      transcript  `json:"proof"`
	Candidates []Candidate `json:"candidates,omitempty"`
	Signature  []byte      `json:"signature"`
}

func sign(key ed25519.PrivateKey, role string, value any) []byte {
	data, err := json.Marshal(value)
	if err != nil || len(key) != ed25519.PrivateKeySize {
		return nil
	}
	return ed25519.Sign(key, append([]byte("blerg-shell-keeper-v2/"+role+"\x00"), data...))
}

func verify(key ed25519.PublicKey, role string, value any, signature []byte) bool {
	if len(key) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		return false
	}
	data, err := json.Marshal(value)
	return err == nil && ed25519.Verify(key, append([]byte("blerg-shell-keeper-v2/"+role+"\x00"), data...), signature)
}

// The single inventory frame uses direction/sequence domain separation. There
// is no post-handshake command stream: close after this one authenticated frame.
func inventoryMAC(key []byte, i inventory) []byte {
	data, _ := json.Marshal(i)
	m := hmac.New(sha256.New, key)
	_, _ = m.Write([]byte("blerg-shell-keeper-v2/keeper-inventory/1\x00"))
	_, _ = m.Write(data)
	return m.Sum(nil)
}

func sessionKey(secret *ecdh.PrivateKey, peer []byte, p transcript) ([]byte, error) {
	public, err := ecdh.X25519().NewPublicKey(peer)
	if err != nil {
		return nil, err
	}
	shared, err := secret.ECDH(public)
	if err != nil {
		return nil, err
	}
	// RFC 5869 HKDF-SHA256, one 32-byte expand block, transcript as info.
	extract := hmac.New(sha256.New, nil)
	_, _ = extract.Write(shared)
	expand := hmac.New(sha256.New, extract.Sum(nil))
	data, _ := json.Marshal(p)
	_, _ = expand.Write(append([]byte("blerg-shell-keeper-v2/session\x00"), data...))
	_, _ = expand.Write([]byte{1})
	return expand.Sum(nil), nil
}

var buildOnce sync.Once
var buildDigest string
var buildError error

// BuildID hashes the running inode, never the pathname a reinstall replaces.
func BuildID() (string, error) {
	buildOnce.Do(func() {
		f, err := os.Open("/proc/self/exe")
		if err != nil {
			buildError = err
			return
		}
		defer func() { _ = f.Close() }()
		h := sha256.New()
		if _, err = io.Copy(h, f); err != nil {
			buildError = err
			return
		}
		buildDigest = hex.EncodeToString(h.Sum(nil))
	})
	return buildDigest, buildError
}

func nonce() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
