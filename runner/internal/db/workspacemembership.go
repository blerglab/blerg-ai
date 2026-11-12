package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Membership phases. Runtime fencing and preparation are journaled by the runtime
// itself; the server's phases decide what is authorized and what is published.
const (
	MembershipRequested  = "requested"  // Main asked; the person has not decided
	MembershipAuthorized = "authorized" // waits for a safe turn boundary
	MembershipCommitted  = "committed"  // published; writers stay fenced until installed
	MembershipInstalled  = "installed"
	MembershipDenied     = "denied"
	MembershipRefused    = "refused"
)

var (
	ErrMembershipStale    = errors.New("workspace membership changed")
	ErrMembershipBusy     = errors.New("another repository change is pending")
	ErrMembershipConflict = errors.New("repository change does not match the authorized request")
)

// MembershipOperation is the authorized journal entry. Proposed is the exact
// manifest an approval binds; Source is private selection input, never a path a
// browser reads back from another person's session.
type MembershipOperation struct {
	ID                  string
	SessionID           string
	Kind                string
	Phase               string
	RequestedBy         string
	Principal           string
	ApprovedBy          string
	ExpectedRevision    int64
	OldDigest           string
	Proposed            protocol.WorkspaceManifest
	MemberID            string
	MemberName          string
	Source              protocol.SelectionRef
	Receipt             string
	Waiting             string
	Refusal             string
	LocalFiles          protocol.WorkspaceLocalFiles // named by a refused removal
	DiscardLocal        string                       // the person's confirmation of those files
	CommittedRevision   int64
	CreatedAt           time.Time
	UpdatedAt           time.Time
	CloneRetryStartedAt *time.Time
}

func (o MembershipOperation) requestDigest() (string, error) {
	raw, err := json.Marshal(struct {
		Session, Kind, Old, Proposed, Member, Discard string
		Expected                                      int64
		Source                                        protocol.SelectionRef
	}{o.SessionID, o.Kind, o.OldDigest, o.Proposed.Digest, o.MemberID, o.DiscardLocal, o.ExpectedRevision, o.Source})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

const membershipColumns = `id::text,session_id::text,kind,phase,requested_by,principal,approved_by,expected_revision,old_digest,proposed,member_id,member_name,source,receipt,waiting,refusal,local_files,discard_local,COALESCE(committed_revision,0),created_at,updated_at,clone_retry_started_at`

func scanMembership(row pgx.Row) (*MembershipOperation, error) {
	var o MembershipOperation
	var proposed, source, local []byte
	err := row.Scan(&o.ID, &o.SessionID, &o.Kind, &o.Phase, &o.RequestedBy, &o.Principal, &o.ApprovedBy, &o.ExpectedRevision, &o.OldDigest, &proposed, &o.MemberID, &o.MemberName, &source, &o.Receipt, &o.Waiting, &o.Refusal, &local, &o.DiscardLocal, &o.CommittedRevision, &o.CreatedAt, &o.UpdatedAt, &o.CloneRetryStartedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(proposed, &o.Proposed); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(source, &o.Source); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(local, &o.LocalFiles); err != nil {
		return nil, err
	}
	return &o, nil
}

// ResolveMembershipClone replaces only an authorized URL proposal, never an
// unapproved request. Its original request digest remains the idempotency key.
func ResolveMembershipClone(ctx context.Context, pool *pgxpool.Pool, id, digest string, resolved MembershipOperation) error {
	if err := resolved.Proposed.Validate(); err != nil {
		return err
	}
	proposed, err := json.Marshal(resolved.Proposed)
	if err != nil {
		return err
	}
	source, err := json.Marshal(resolved.Source)
	if err != nil {
		return err
	}
	result, err := pool.Exec(ctx, `UPDATE workspace_membership_operations SET proposed=$3,proposed_digest=$4,member_id=$5,source=$6,updated_at=now() WHERE id=$1 AND proposed_digest=$2 AND phase='authorized' AND source->>'git_url'<>''`, id, digest, proposed, resolved.Proposed.Digest, resolved.MemberID, source)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrMembershipStale
	}
	return nil
}

// CreateMembershipOperation is the revision compare-and-set. A retry with the
// same ID must match the original request exactly and returns created=false.
func CreateMembershipOperation(ctx context.Context, pool *pgxpool.Pool, o MembershipOperation) (*MembershipOperation, bool, error) {
	if o.Phase != MembershipRequested && o.Phase != MembershipAuthorized {
		return nil, false, ErrMembershipConflict
	}
	if err := o.Proposed.Validate(); err != nil {
		return nil, false, err
	}
	digest, err := o.requestDigest()
	if err != nil {
		return nil, false, err
	}
	proposed, err := json.Marshal(o.Proposed)
	if err != nil {
		return nil, false, err
	}
	source, err := json.Marshal(o.Source)
	if err != nil {
		return nil, false, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is harmless
	var revision int64
	if err = tx.QueryRow(ctx, `SELECT membership_revision FROM session_workspaces WHERE session_id=$1 FOR UPDATE`, o.SessionID).Scan(&revision); err != nil {
		return nil, false, err
	}
	var existing string
	switch err = tx.QueryRow(ctx, `SELECT request_digest FROM workspace_membership_operations WHERE id=$1 AND session_id=$2`, o.ID, o.SessionID).Scan(&existing); {
	case err == nil:
		if existing != digest {
			return nil, false, ErrMembershipConflict
		}
		same, e := scanMembership(tx.QueryRow(ctx, `SELECT `+membershipColumns+` FROM workspace_membership_operations WHERE id=$1`, o.ID))
		return same, false, e
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, false, err
	}
	if revision != o.ExpectedRevision || o.Proposed.Revision != revision+1 {
		return nil, false, ErrMembershipStale
	}
	var open bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_membership_operations WHERE session_id=$1 AND phase IN ('requested','authorized','committed'))`, o.SessionID).Scan(&open); err != nil {
		return nil, false, err
	}
	if open {
		return nil, false, ErrMembershipBusy
	}
	if _, err = tx.Exec(ctx, `INSERT INTO workspace_membership_operations(id,session_id,kind,phase,requested_by,principal,approved_by,expected_revision,old_digest,proposed_digest,proposed,member_id,member_name,source,request_digest,discard_local) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, o.ID, o.SessionID, o.Kind, o.Phase, o.RequestedBy, o.Principal, o.ApprovedBy, o.ExpectedRevision, o.OldDigest, o.Proposed.Digest, proposed, o.MemberID, o.MemberName, source, digest, o.DiscardLocal); err != nil {
		return nil, false, err
	}
	created, err := scanMembership(tx.QueryRow(ctx, `SELECT `+membershipColumns+` FROM workspace_membership_operations WHERE id=$1`, o.ID))
	if err != nil {
		return nil, false, err
	}
	return created, true, tx.Commit(ctx)
}

func GetMembershipOperation(ctx context.Context, pool *pgxpool.Pool, id string) (*MembershipOperation, error) {
	return scanMembership(pool.QueryRow(ctx, `SELECT `+membershipColumns+` FROM workspace_membership_operations WHERE id=$1`, id))
}

// OpenMembershipOperation returns the session's undecided, pending or uninstalled change.
func OpenMembershipOperation(ctx context.Context, pool *pgxpool.Pool, session string) (*MembershipOperation, error) {
	return scanMembership(pool.QueryRow(ctx, `SELECT `+membershipColumns+` FROM workspace_membership_operations WHERE session_id=$1 AND phase IN ('requested','authorized','committed')`, session))
}

// LatestMembershipOperation lets the tab explain the outcome of the last change.
func LatestMembershipOperation(ctx context.Context, pool *pgxpool.Pool, session string) (*MembershipOperation, error) {
	return scanMembership(pool.QueryRow(ctx, `SELECT `+membershipColumns+` FROM workspace_membership_operations WHERE session_id=$1 ORDER BY created_at DESC, id DESC LIMIT 1`, session))
}

// OpenMembershipOperations lists sessions with a change a runtime may apply; a
// retry is safe because every step is idempotent.
func OpenMembershipOperations(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT session_id::text FROM workspace_membership_operations WHERE phase IN ('authorized','committed')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// DecideMembershipOperation binds a person's decision to the exact proposal.
func DecideMembershipOperation(ctx context.Context, pool *pgxpool.Pool, id, person, proposedDigest string, allow bool) error {
	phase, refusal := MembershipDenied, ""
	if allow {
		phase = MembershipAuthorized
	}
	tag, err := pool.Exec(ctx, `UPDATE workspace_membership_operations SET phase=$2,approved_by=$3,refusal=$4,updated_at=now() WHERE id=$1 AND phase='requested' AND proposed_digest=$5`, id, phase, person, refusal, proposedDigest)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrMembershipConflict
	}
	return err
}

// SetMembershipWaiting records why a change has not applied or finished yet.
func SetMembershipWaiting(ctx context.Context, pool *pgxpool.Pool, id, reason string) error {
	_, err := pool.Exec(ctx, `UPDATE workspace_membership_operations SET waiting=$2,updated_at=now() WHERE id=$1 AND phase IN ('authorized','committed') AND waiting<>$2`, id, reason)
	return err
}

// RefuseMembershipOperation closes a change that was never published. A
// committed operation can only finish installing; it never restores old membership.
func RefuseMembershipOperation(ctx context.Context, pool *pgxpool.Pool, id, reason string) error {
	return RefuseMembershipRemoval(ctx, pool, id, reason, protocol.WorkspaceLocalFiles{})
}

// RefuseMembershipRemoval also keeps the local files that stopped a removal, for
// the row that names them and the confirmation that binds them.
func RefuseMembershipRemoval(ctx context.Context, pool *pgxpool.Pool, id, reason string, local protocol.WorkspaceLocalFiles) error {
	raw, err := json.Marshal(local)
	if err != nil {
		return err
	}
	tag, err := pool.Exec(ctx, `UPDATE workspace_membership_operations SET phase='refused',refusal=$2,local_files=$3,waiting='',updated_at=now() WHERE id=$1 AND phase IN ('requested','authorized')`, id, reason, raw)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrMembershipConflict
	}
	return err
}

// CommitMembershipOperation atomically publishes the revision, the binding of the
// new digest and the outcome. The caller has proved writers fenced; the receipt
// binds this commit to the runtime's prepared allocation, and an exact retry after
// a lost reply returns the committed binding.
func CommitMembershipOperation(ctx context.Context, pool *pgxpool.Pool, id string, binding protocol.WorkspaceRuntimeBinding, receipt string) (protocol.WorkspaceRuntimeBinding, error) {
	var zero protocol.WorkspaceRuntimeBinding
	if receipt == "" {
		return zero, ErrMembershipConflict
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is harmless
	var session string
	if err = tx.QueryRow(ctx, `SELECT session_id::text FROM workspace_membership_operations WHERE id=$1`, id).Scan(&session); err != nil {
		return zero, ErrMembershipConflict
	}
	var revision, generation int64
	var launch string
	var rawBinding, rawResults []byte
	var ended bool
	err = tx.QueryRow(ctx, `SELECT w.membership_revision,w.runtime_generation,w.launch_generation,w.runtime_binding,w.results,s.ended_at IS NOT NULL OR s.status IN ('stopped','ended') FROM session_workspaces w JOIN sessions s ON s.id=w.session_id WHERE w.session_id=$1 FOR UPDATE OF w`, session).Scan(&revision, &generation, &launch, &rawBinding, &rawResults, &ended)
	if err != nil {
		return zero, err
	}
	o, err := scanMembership(tx.QueryRow(ctx, `SELECT `+membershipColumns+` FROM workspace_membership_operations WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return zero, err
	}
	var current protocol.WorkspaceRuntimeBinding
	if rawBinding != nil {
		if err = json.Unmarshal(rawBinding, &current); err != nil {
			return zero, err
		}
	}
	if o.Phase == MembershipCommitted || o.Phase == MembershipInstalled {
		if o.Receipt != receipt || current.WorkspaceRuntimeRef != binding.WorkspaceRuntimeRef || current.Root != binding.Root || current.ManifestDigest != o.Proposed.Digest {
			return zero, ErrMembershipConflict
		}
		return current, nil
	}
	if o.Phase != MembershipAuthorized || o.Source.GitURL != "" {
		return zero, ErrMembershipConflict
	}
	if ended || rawBinding == nil || current != binding || binding.Generation != generation || binding.LaunchGeneration != launch || binding.ManifestDigest != o.OldDigest || revision != o.ExpectedRevision || o.Proposed.Revision != revision+1 || o.Proposed.LaunchGeneration != launch {
		return zero, ErrMembershipStale
	}
	var results []protocol.WorkspaceMemberResult
	if err = json.Unmarshal(rawResults, &results); err != nil {
		return zero, err
	}
	kept := []protocol.WorkspaceMemberResult{}
	retired := []protocol.WorkspaceMemberResult{}
	for _, r := range results {
		if o.Kind == "remove" && r.MemberID == o.MemberID {
			retired = append(retired, r)
			continue
		}
		kept = append(kept, r)
	}
	if err = o.Proposed.ValidateResults(kept); err != nil {
		return zero, err
	}
	next := binding
	next.ManifestDigest = o.Proposed.Digest
	if err = next.Validate(o.Proposed); err != nil {
		return zero, err
	}
	nextRaw, err := json.Marshal(next)
	if err != nil {
		return zero, err
	}
	manifest, err := json.Marshal(o.Proposed)
	if err != nil {
		return zero, err
	}
	keptRaw, err := json.Marshal(kept)
	if err != nil {
		return zero, err
	}
	retiredRaw, err := json.Marshal(retired)
	if err != nil {
		return zero, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO session_workspace_revisions(session_id,revision,manifest,operation_id,retired_results) VALUES($1,$2,$3,$4,$5)`, session, o.Proposed.Revision, manifest, id, retiredRaw); err != nil {
		return zero, err
	}
	if _, err = tx.Exec(ctx, `UPDATE session_workspaces SET membership_revision=$2,runtime_binding=$3,results=$4 WHERE session_id=$1`, session, o.Proposed.Revision, nextRaw, keptRaw); err != nil {
		return zero, err
	}
	if _, err = tx.Exec(ctx, `UPDATE sessions SET workspace_kind=$2 WHERE id=$1`, session, o.Proposed.Kind); err != nil {
		return zero, err
	}
	if _, err = tx.Exec(ctx, `UPDATE workspace_membership_operations SET phase='committed',receipt=$2,committed_revision=$3,waiting='',updated_at=now() WHERE id=$1`, id, receipt, o.Proposed.Revision); err != nil {
		return zero, err
	}
	return next, tx.Commit(ctx)
}

// CompleteMembershipOperation records the runtime's install acknowledgment.
// Repeating it after a lost reply is harmless.
func CompleteMembershipOperation(ctx context.Context, pool *pgxpool.Pool, id string) error {
	tag, err := pool.Exec(ctx, `UPDATE workspace_membership_operations SET phase='installed',waiting='',updated_at=now() WHERE id=$1 AND phase IN ('committed','installed')`, id)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrMembershipConflict
	}
	return err
}

// GetSessionLaunchManifest reads the immutable launch selection, independent of
// the current membership revision.
func GetSessionLaunchManifest(ctx context.Context, pool *pgxpool.Pool, id string) (*protocol.WorkspaceManifest, error) {
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT manifest FROM session_workspaces WHERE session_id=$1`, id).Scan(&raw); err != nil {
		return nil, err
	}
	var m protocol.WorkspaceManifest
	return &m, json.Unmarshal(raw, &m)
}

// MembershipRetiredResults keeps the delivery outcomes of removed members addressable.
func MembershipRetiredResults(ctx context.Context, pool *pgxpool.Pool, id string) ([]protocol.WorkspaceMemberResult, error) {
	rows, err := pool.Query(ctx, `SELECT retired_results FROM session_workspace_revisions WHERE session_id=$1 ORDER BY revision`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.WorkspaceMemberResult
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var batch []protocol.WorkspaceMemberResult
		if err := json.Unmarshal(raw, &batch); err != nil {
			return nil, err
		}
		out = append(out, batch...)
	}
	return out, rows.Err()
}

// WorkspaceMemberInUse reports a live descendant whose own selection holds the
// member. Running workers keep what they have; removal waits for them.
func WorkspaceMemberInUse(ctx context.Context, pool *pgxpool.Pool, session, member string) (bool, error) {
	var busy bool
	err := pool.QueryRow(ctx, `WITH RECURSIVE tree AS (
 SELECT id FROM sessions WHERE parent_session_id=$1
 UNION SELECT s.id FROM sessions s JOIN tree t ON s.parent_session_id=t.id
) SELECT EXISTS(SELECT 1 FROM tree t JOIN sessions s ON s.id=t.id JOIN session_workspaces w ON w.session_id=s.id, jsonb_array_elements(w.manifest->'members') m WHERE s.ended_at IS NULL AND s.archived_at IS NULL AND s.status NOT IN ('stopped','ended') AND m->>'id'=$2)`, session, member).Scan(&busy)
	return busy, err
}

// SetMembershipCloneWaiting starts a bounded recovery window once. Repeated
// sweeps, reason changes and server restarts cannot extend that window.
func SetMembershipCloneWaiting(ctx context.Context, pool *pgxpool.Pool, id string, now time.Time, reason string) error {
	_, err := pool.Exec(ctx, `UPDATE workspace_membership_operations SET clone_retry_started_at=COALESCE(clone_retry_started_at,$2),waiting=$3,updated_at=now() WHERE id=$1 AND phase='authorized' AND source->>'git_url'<>'' AND (clone_retry_started_at IS NULL OR waiting<>$3)`, id, now, reason)
	return err
}
