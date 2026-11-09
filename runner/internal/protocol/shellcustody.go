package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

const ShellPersistenceVersion = 2
const DesktopShellLifetime = 7 * 24 * time.Hour

// DesktopShellClockSkew permits the same small wall-clock offset as access
// grants. It never extends the immutable runner-issued custody deadline.
const DesktopShellClockSkew = 5 * time.Second

// ShellCustody is retained only in the runner and runtime's private memory for
// #269. A database row, browser handle, or matching PID cannot reconstruct it.
// Its digest is authenticated by the access grant; expected fields must still
// come from trusted runtime preparation and original human authorization.
type ShellCustody struct {
	Privileged        bool          `json:"privileged,omitempty"`
	Container         string        `json:"container_identity,omitempty"`
	Version           int           `json:"version"`
	Owner             string        `json:"owner"`
	Login             string        `json:"login"`
	Session           string        `json:"session"`
	Launch            string        `json:"launch"`
	Runtime           string        `json:"runtime"`
	RuntimeGeneration int64         `json:"runtime_generation"`
	Root              WorkspaceRoot `json:"root"`
	Manifest          string        `json:"manifest"`
	Desktop           string        `json:"desktop"`
	Credential        string        `json:"credential_id"`
	OwnershipRevision int64         `json:"ownership_revision"`
	Runner            string        `json:"runner_incarnation"`
	Daemon            string        `json:"daemon_incarnation"`
	Shell             string        `json:"shell"`
	Nonce             string        `json:"nonce"`
	Identity          string        `json:"identity_digest"`
	Consent           string        `json:"consent,omitempty"`
	Created           int64         `json:"created"`
	LifetimeUntil     int64         `json:"lifetime_until"`
}

func (c ShellCustody) Validate() error {
	if c.Privileged && c.Runtime != "host" {
		return errors.New("privileged shell requires host runtime")
	}
	if c.Version != ShellPersistenceVersion || (c.Runtime != "host" && c.Runtime != "sandbox") || c.RuntimeGeneration <= 0 || c.Root.Inode == 0 || c.OwnershipRevision <= 0 || c.Created <= 0 || c.LifetimeUntil <= c.Created || c.LifetimeUntil-c.Created > int64(87600*time.Hour/time.Second) || len(c.Container) > 256 || c.Runtime == "sandbox" && c.Container == "" || c.Runtime == "host" && (c.Consent == "" || c.LifetimeUntil-c.Created > int64(DesktopShellLifetime/time.Second)) {
		return errors.New("invalid shell custody")
	}
	for _, v := range []string{c.Owner, c.Login, c.Session, c.Launch, c.Manifest, c.Desktop, c.Credential, c.Runner, c.Daemon, c.Shell, c.Nonce, c.Identity} {
		if v == "" || len(v) > 256 {
			return errors.New("invalid shell custody")
		}
	}
	if len(c.Consent) > 256 {
		return errors.New("invalid shell custody")
	}
	return nil
}

func custodyDigest(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (c ShellCustody) Digest() string {
	if c.Validate() != nil {
		return ""
	}
	return custodyDigest(c)
}

// PhysicalScope deliberately excludes credentials, process incarnations, shell
// IDs and consent: changing any of those cannot hide old writers on this exact
// prepared runtime. Full custody equality remains mandatory for adoption.
func (c ShellCustody) PhysicalScope() string {
	return custodyDigest(struct {
		Session, Launch, Runtime, Manifest, Desktop, Container string
		Generation                                             int64
		Root                                                   WorkspaceRoot
	}{c.Session, c.Launch, c.Runtime, c.Manifest, c.Desktop, c.Container, c.RuntimeGeneration, c.Root})
}

// CustodyCleanup settles only a retained allocation, independently of the
// access connection in its last grant. The receiver must also authenticate and
// correlate the current transport before accepting this receipt.
type CustodyCleanup struct {
	Version          int    `json:"version"`
	Custody          string `json:"custody"`
	Scope            string `json:"scope"`
	Transaction      string `json:"transaction"`
	Responder        string `json:"responder"`
	ContainmentEmpty bool   `json:"containment_empty"`
	PTYsClosed       bool   `json:"ptys_closed"`
	Reaped           bool   `json:"reaped"`
	OutputJoined     bool   `json:"output_joined"`
}

func (p CustodyCleanup) ValidateFor(c ShellCustody, transaction, responder string) error {
	if c.Validate() != nil || p.Version != 2 || transaction == "" || responder == "" || p.Custody != c.Digest() || p.Scope != c.PhysicalScope() || p.Transaction != transaction || p.Responder != responder || !p.ContainmentEmpty || !p.PTYsClosed || !p.Reaped || !p.OutputJoined {
		return errors.New("invalid shell cleanup receipt")
	}
	return nil
}
