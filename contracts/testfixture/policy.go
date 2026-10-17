// Package testfixture defines the opt-in identity of disposable integration-test databases.
// It has no PostgreSQL dependency: adapters must additionally hold the run lease and reject
// active database connections before attempting a plain (never FORCE) DROP.
package testfixture

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const Prefix = "blerg-testfixture/v1:"
const MaxCommentBytes = 2048

type Scope struct {
	Database    string `json:"database"`
	DatabaseOID uint32 `json:"database_oid"`
	Role        string `json:"role"`
	RoleOID     uint32 `json:"role_oid"`
}
type Metadata struct {
	Version     int       `json:"version"`
	Module      string    `json:"module"`
	Database    string    `json:"database"`
	DatabaseOID uint32    `json:"database_oid"`
	RunID       string    `json:"run_id"`
	CreatedAt   time.Time `json:"created_at"`
	Scope       Scope     `json:"scope"`
}
type Candidate struct {
	Database    string
	DatabaseOID uint32
	OwnerOID    uint32
	Comment     string
	Template    bool
}

func hexID(s string) bool {
	if len(s) != 24 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
func valid(m Metadata) bool {
	if m.Module != "runner" && m.Module != "core" && m.Module != "board" {
		return false
	}
	prefix := "cx_" + m.Module + "_"
	_, offset := m.CreatedAt.Zone()
	return m.Version == 1 && strings.HasPrefix(m.Database, prefix) && hexID(strings.TrimPrefix(m.Database, prefix)) && hexID(m.RunID) && m.DatabaseOID > 0 && m.Scope.DatabaseOID > 0 && m.Scope.RoleOID > 0 && m.Scope.Database != "" && m.Scope.Role != "" && m.DatabaseOID != m.Scope.DatabaseOID && m.Database != m.Scope.Database && !m.CreatedAt.IsZero() && offset == 0
}

// Encode creates the exact canonical comment accepted by Eligible. Unknown, unmarked, legacy or
// altered comments are never cleanup authorization.
func Encode(m Metadata) (string, error) {
	if !valid(m) {
		return "", errors.New("invalid test database identity")
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	comment := Prefix + string(raw)
	if len(comment) > MaxCommentBytes {
		return "", errors.New("test database identity too long")
	}
	return comment, nil
}

// Eligible checks every stored identity component and requires a minimum age of one minute.
// A caller must still acquire the run lease and recheck this exact catalog identity before DROP.
func Eligible(c Candidate, scope Scope, module string, now time.Time, minAge time.Duration) (Metadata, bool) {
	var m Metadata
	if minAge < time.Minute || c.Template || len(c.Comment) > MaxCommentBytes || !strings.HasPrefix(c.Comment, Prefix) || c.Database == scope.Database || c.DatabaseOID == scope.DatabaseOID {
		return m, false
	}
	if json.Unmarshal([]byte(strings.TrimPrefix(c.Comment, Prefix)), &m) != nil {
		return m, false
	}
	canonical, err := Encode(m)
	if err != nil || canonical != c.Comment || m.Module != module || m.Scope != scope || m.Database != c.Database || m.DatabaseOID != c.DatabaseOID || c.OwnerOID != scope.RoleOID || now.Before(m.CreatedAt) || now.Sub(m.CreatedAt) < minAge {
		return Metadata{}, false
	}
	return m, true
}

// LeaseKey is a process-run lease shared by all databases created in the same admin scope.
// It contains no connection string, endpoint, password or credential.
func LeaseKey(module, runID string, scope Scope) int64 {
	raw, _ := json.Marshal(struct {
		Module, RunID string
		Scope         Scope
	}{module, runID, scope})
	sum := sha256.Sum256(append([]byte("blerg/testfixture/run/v1\x00"), raw...))
	return int64(binary.BigEndian.Uint64(sum[:8]) >> 1)
}
