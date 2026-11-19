package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/contracts/livetls"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type livePersonTokenKey struct{}

func withLivePersonToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, livePersonTokenKey{}, token)
}
func livePersonToken(ctx context.Context) string {
	token, _ := ctx.Value(livePersonTokenKey{}).(string)
	return token
}

// Never redirects, persists, logs or sends the forwarded token to an engine.
// This path cannot call the session-less issuer or resolve a connection secret.
func (a *API) liveCore(ctx context.Context, path string, body, out any) error {
	if path != "/internal/live-data/proposals" && path != "/internal/live-data/approve" && path != "/internal/live-data/receipt" && path != "/internal/live-data/confirm" && path != "/internal/live-data/cancel" {
		return boundFailure("invalid_subject")
	}
	if a.liveDataCoreURL == "" || a.coreInternalKey == "" {
		return boundFailure("live_data_unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := json.Marshal(body)
	if err != nil {
		return boundFailure("invalid_subject")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", a.liveDataCoreURL+path, bytes.NewReader(raw)) // #nosec G704 -- configured Core only; callers use fixed internal approval paths.
	if err != nil {
		return boundFailure("live_data_unavailable")
	}
	req.Header.Set("Authorization", "Bearer "+a.coreInternalKey)
	req.Header.Set("Content-Type", "application/json")
	response, err := coreHTTPClient(a.liveDataClient).Do(req) // #nosec G704 -- target is the configured Core service; redirects are disabled.
	if err != nil {
		if livetls.Refused(err) {
			return boundFailure("live_service_tls_refused")
		}
		return boundFailure("live_data_unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if path == "/internal/live-data/confirm" && response.StatusCode == 409 && response.Header.Get("X-Blerg-Live-Data-Cause") == "decision_revoked" {
		return boundFailure("decision_revoked")
	}
	if response.Header.Get("X-Blerg-Live-Data-Cause") == "service_identity_required" || response.Header.Get("X-Blerg-Live-Data-Cause") == "runner_identity_mismatch" {
		return boundFailure("live_service_identity_required")
	}
	if response.StatusCode == 401 {
		return boundFailure("fresh_login_required")
	}
	if response.StatusCode != 200 {
		return boundFailure("live_data_decision_refused")
	}
	raw, err = io.ReadAll(io.LimitReader(response.Body, 96*1024+1))
	if err != nil || len(raw) > 96*1024 || json.Unmarshal(raw, out) != nil {
		return boundFailure("live_data_unavailable")
	}
	return nil
}

func (a *API) createBoundLiveAsk(ctx context.Context, req postMessageRequest, session *db.SessionRow) (string, error) {
	if req.Subject.LiveBinding == nil {
		return "", boundFailure("invalid_subject")
	}
	if !req.Subject.LiveBinding.Card.Native() {
		return "", boundFailure("unsupported_live_card")
	}
	if !a.liveDataPollingEnabled {
		return "", boundFailure("live_data_disabled")
	}
	config := *req.Subject.LiveBinding
	owner := *session.SpawningAccountID
	if config.Scope.Owner != owner {
		return "", boundFailure("wrong_owner")
	}
	grants, err := db.ListMCPGrantsForSession(ctx, a.dbPool, session.ID)
	if err != nil {
		return "", boundFailure("storage_unavailable")
	}
	var selected *db.MCPGrant
	for i := range grants {
		g := &grants[i]
		tool, ok := g.Tools[config.Scope.Tool]
		if g.ConnectionID == config.Scope.Connection && g.AccountID == owner && g.Builtin == "" && ok && tool.Mode == "allow" && tool.Hash == config.Scope.Hash {
			selected = g
			break
		}
	}
	if selected == nil {
		return "", boundFailure("live_tool_not_allowed")
	}
	definition, err := db.GetToolDefinition(ctx, a.dbPool, config.Scope.Hash)
	if err != nil || definition.Unsafe != "" {
		return "", boundFailure("live_tool_not_allowed")
	}
	proof := mcpgw.Proof{AccountID: owner}
	if selected.ProofKind == "session_id" {
		proof.SessionID = selected.ProofValue
	} else {
		proof.TokenID = selected.ProofValue
	}
	fresh, err := a.liveFreshDefinition(ctx, proof, selected.ConnectionID, selected.URLSnapshot, config.Scope.Tool, config.Scope.Hash)
	if err != nil {
		return "", err
	}
	config.ToolDefinition = fresh
	askID := newUUID()
	body := map[string]any{"account_id": owner, "configuration": config, "ask_id": askID}
	switch selected.ProofKind {
	case "session_id":
		body["session_id"] = selected.ProofValue
	case "token_id":
		body["token_id"] = selected.ProofValue
	default:
		return "", boundFailure("human_login_required")
	}
	var proposal ld.Proposal
	if err = a.liveCore(ctx, "/internal/live-data/proposals", body, &proposal); err != nil {
		return "", err
	}
	config = proposal.Configuration
	if config.Scope.Owner != owner || config.Scope.Binding != req.Subject.LiveBinding.Scope.Binding || config.Scope.Revision != req.Subject.LiveBinding.Scope.Revision || proposal.AskID != askID || proposal.Status != "pending" || proposal.Digest != config.Scope.Digest {
		return "", boundFailure("invalid_subject")
	}
	normalized, _, err := config.Canonical(time.Now())
	if err != nil || normalized.Scope.Digest != proposal.Digest {
		return "", boundFailure("invalid_subject")
	}
	subject := boundSubject{Kind: "live_binding", Revision: int(config.Scope.Revision), Digest: proposal.Digest, LiveBinding: &config}
	mapping, _ := json.MarshalIndent(config.Mapping, "", "  ")
	text := fmt.Sprintf("Approve live binding · %s\n\nBinding %s · revision %d\nRunner: %s\nInstallation: %s\nConnection: %s\nTool: %s\nTool hash: %s\nRevision digest: %s\nArguments: %s\nTool definition: %s\nMapping:\n%s\nInterval: %d seconds\nVisibility: private to you\nExpires: %s\n\nFresh listing: readOnlyHint = true. The provider's read-only claim is untrusted; polling rechecks the claim and exact pin before each call. A separate live-data allow policy and any required machine consent must already be set in Settings.\n\nReply to this question with yes to approve this exact revision, or no to decline. Polling is not available yet.", boundSummaryField(config.Scope.Tool), config.Scope.Binding, config.Scope.Revision, config.Scope.Runner, config.Scope.Installation, boundSummaryField(selected.Name), boundSummaryField(config.Scope.Tool), config.Scope.Hash, proposal.Digest, config.Arguments, config.ToolDefinition, mapping, config.IntervalSeconds, config.ExpiresAt.Format(time.RFC3339))
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = db.LockDraftWorkspace(ctx, tx, session.ID); err != nil {
		return "", err
	}
	return a.persistBoundAsk(ctx, tx, session.ID, owner, subject, text, req.NoWait, req.Urgent, req.SessionPosted, askID)
}

func (a *API) answerBoundLiveAsk(ctx context.Context, msg protocol.AgentUserMessage, p identity.Principal, id string) (bool, error) {
	preflight, err := a.preflightLiveAnswer(ctx, msg, p, id)
	if err != nil {
		return true, err
	}
	tx, err := a.dbPool.Begin(ctx)
	if err != nil {
		return true, boundFailure("storage_unavailable")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = db.LockDraftWorkspace(ctx, tx, msg.SessionID); err != nil {
		return true, err
	}
	var subject boundSubject
	var raw []byte
	var owner, sid, status string
	var expired bool
	var epoch int64
	err = tx.QueryRow(ctx, `SELECT subject,owner_id,workspace_id::text,status,expires_at<=clock_timestamp(),session_epoch FROM bound_asks WHERE message_id=$1 FOR UPDATE`, id).Scan(&raw, &owner, &sid, &status, &expired, &epoch)
	if err != nil || json.Unmarshal(raw, &subject) != nil || subject.Kind != "live_binding" || subject.LiveBinding == nil {
		return true, boundFailure("invalid_subject")
	}
	if !bytes.Equal(raw, preflight) {
		return true, boundFailure("stale_revision")
	}
	if owner != p.Sub || sid != msg.SessionID {
		return true, boundFailure("wrong_owner")
	}
	answer := strings.ToLower(strings.TrimSpace(msg.Text))
	revoking := status == "committed" && (answer == "no" || answer == "decline" || answer == "")
	if status != "open" && !revoking {
		var prior string
		if tx.QueryRow(ctx, `SELECT coalesce(answer,'') FROM messages WHERE id=$1`, id).Scan(&prior) == nil && prior == msg.Text && (status == "committed" || status == "declined") {
			_ = tx.Rollback(ctx)
			return true, a.reconcileLiveDecisions(ctx, id)
		}
		return true, boundFailure("already_decided")
	}
	var open bool
	if err = tx.QueryRow(ctx, `SELECT status='open' FROM messages WHERE id=$1 FOR UPDATE`, id).Scan(&open); err != nil {
		return true, err
	}
	var liveEpoch int64
	if err = tx.QueryRow(ctx, `SELECT bound_ask_epoch FROM sessions WHERE id=$1 FOR SHARE`, sid).Scan(&liveEpoch); err != nil {
		return true, err
	}
	session, err := db.GetSession(ctx, tx, sid)
	if err != nil {
		return true, err
	}
	if (!open || expired) && !revoking {
		return true, a.cancelStaleBoundAsk(ctx, tx, id, "expired_or_closed")
	}
	if session == nil || boundSessionEnded(session) || session.ArchivedAt != nil || session.DiscardRequestedAt != nil || epoch != liveEpoch || session.SpawningAccountID == nil || *session.SpawningAccountID != owner {
		return true, a.cancelStaleBoundAsk(ctx, tx, id, "session_invalidated")
	}
	state := "canceled"
	switch answer {
	case "yes", "approve":
		state = "committed"
	case "no", "decline":
		state = "declined"
	case "":
	default:
		_ = tx.Rollback(ctx)
		_ = a.boundAskNote(ctx, id, "Not applied: reply yes or no to this question, or ask Main to revise it.")
		return true, boundConversation("ambiguous_answer")
	}
	config := subject.LiveBinding
	if state == "committed" {
		normalized, _, validationErr := config.Canonical(time.Now())
		if validationErr != nil || normalized.Scope.Digest != subject.Digest || config.Scope.Revision != int64(subject.Revision) || config.Scope.Owner != owner {
			return true, a.cancelStaleBoundAsk(ctx, tx, id, "stale_revision")
		}
	}

	receipt, _ := json.Marshal(map[string]any{"ask_id": id, "binding_id": config.Scope.Binding, "revision": subject.Revision, "digest": subject.Digest, "status": state})
	if _, err = tx.Exec(ctx, `UPDATE bound_asks SET status=$2,decision=$3,decision_input_id=NULLIF($4,'') WHERE message_id=$1`, id, state, receipt, msg.ClientID); err != nil {
		return true, err
	}
	if _, err = tx.Exec(ctx, `UPDATE messages SET status='answered',answer=$2,answered_at=clock_timestamp(),answered_by=$3 WHERE id=$1`, id, msg.Text, "person:"+p.Sub); err != nil {
		return true, err
	}
	payload, _ := json.Marshal(askAnswerPayload{Text: msg.Text, Source: "ask_answer", MessageID: id, ClientID: msg.ClientID})
	at := time.Now()
	eventID := messageEventID(id, "answer")
	if revoking {
		eventID = messageEventID(id, "revoke")
	}
	seq, _, err := db.AppendAgentEventTx(ctx, tx, sid, eventID, "user_message", string(payload), at)
	if err != nil {
		return true, err
	}
	if err = tx.Commit(ctx); err != nil {
		return true, err
	}
	fanOutServerEvent(a.hub, sid, eventID, "user_message", payload, seq, at)
	a.hub.SignalAskAnswered(id)
	if row, err := db.GetMessage(ctx, a.dbPool, id); err == nil && row != nil {
		a.hub.BroadcastJSON(protocol.MessageAnswered{Type: "message_answered", Message: messageRowToInfo(*row)}) //nolint:contextcheck // Existing viewer-filtered broadcast API has no context parameter.
	}
	return true, a.reconcileLiveDecisions(ctx, id)
}

// SetLiveDataApprovalClient configures the dedicated service certificate transport.
func (a *API) SetLiveDataApprovalClient(client *http.Client) { a.liveDataClient = client }

// Preflight performs all upstream I/O before acquiring the ask/workspace locks.
// Core approval remains unusable until the locked local decision is committed
// and its durable outbox confirmation is delivered.
func (a *API) preflightLiveAnswer(ctx context.Context, msg protocol.AgentUserMessage, p identity.Principal, id string) ([]byte, error) {
	var raw []byte
	var owner, sid, status string
	var subject boundSubject
	var epoch, liveEpoch int64
	var open bool
	var expired bool
	err := a.dbPool.QueryRow(ctx, `SELECT b.subject,b.owner_id,b.workspace_id::text,b.status,b.session_epoch,s.bound_ask_epoch,b.expires_at<=clock_timestamp(),m.status='open' FROM bound_asks b JOIN sessions s ON s.id=b.workspace_id JOIN messages m ON m.id=b.message_id WHERE b.message_id=$1`, id).Scan(&raw, &owner, &sid, &status, &epoch, &liveEpoch, &expired, &open)
	if err != nil || json.Unmarshal(raw, &subject) != nil || subject.LiveBinding == nil || subject.Kind != "live_binding" {
		return nil, boundFailure("invalid_subject")
	}
	if owner != p.Sub || sid != msg.SessionID {
		return nil, boundFailure("wrong_owner")
	}
	answer := strings.ToLower(strings.TrimSpace(msg.Text))
	revoking := status == "committed" && (answer == "no" || answer == "decline" || answer == "")
	if status != "open" && !revoking {
		return raw, nil
	} // Local replay checks follow under lock.
	session, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil {
		return nil, boundFailure("storage_unavailable")
	}
	if (expired || !open) && !revoking || epoch != liveEpoch || session == nil || boundSessionEnded(session) || session.ArchivedAt != nil || session.DiscardRequestedAt != nil || session.SpawningAccountID == nil || *session.SpawningAccountID != owner {
		return raw, nil
	} // Locked path closes the stale ask.
	state := "canceled"
	switch answer {
	case "yes", "approve":
		state = "committed"
	case "no", "decline":
		state = "declined"
	case "":
	default:
		return raw, nil
	}
	config := subject.LiveBinding
	if state != "canceled" {
		if livePersonToken(ctx) == "" {
			return nil, boundFailure("human_login_required")
		}
		normalized, _, err := config.Canonical(time.Now())
		if state == "committed" && (err != nil || normalized.Scope.Digest != subject.Digest || config.Scope.Revision != int64(subject.Revision) || config.Scope.Owner != owner) {
			return nil, boundFailure("stale_revision")
		}
		if state == "committed" {
			grants, err := db.ListMCPGrantsForSession(ctx, a.dbPool, sid)
			if err != nil {
				return nil, boundFailure("live_tool_unavailable")
			}
			url := ""
			for _, g := range grants {
				if g.ConnectionID == config.Scope.Connection {
					url = g.URLSnapshot
				}
			}
			if _, err = a.liveFreshDefinition(ctx, mcpgw.Proof{AccountID: owner, SessionID: p.Sid}, config.Scope.Connection, url, config.Scope.Tool, config.Scope.Hash); err != nil {
				return nil, err
			}
		}
		decision := ld.Decision{AccountID: p.Sub, AccessToken: livePersonToken(ctx), BindingID: config.Scope.Binding, Revision: config.Scope.Revision, Digest: subject.Digest, AskID: id, Approve: state == "committed"}

		want := "declined"
		if state == "committed" {
			want = "approved"
		}
		if err = a.liveDecision(ctx, "/internal/live-data/approve", decision, want); err != nil {
			return nil, err
		}
	} else {
		decision := ld.Decision{AccountID: owner, BindingID: config.Scope.Binding, Revision: config.Scope.Revision, Digest: subject.Digest, AskID: id}
		if err = a.liveDecision(ctx, "/internal/live-data/cancel", decision, "declined"); err != nil {
			return nil, err
		}
	}

	return raw, nil
}

// SetLiveDataApprovalURL configures dedicated service discovery, separately from public Core.
func (a *API) SetLiveDataApprovalURL(url string) { a.liveDataCoreURL = url }

// SetLiveDataAuthority stores the in-process issuer client; it exposes no route.
func (a *API) SetLiveDataAuthority(client *ld.ServiceClient) {
	a.liveDataAuthority = client
	if a.hub != nil {
		a.hub.liveDataAuthority = client
	}
}
