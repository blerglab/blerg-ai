package server

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkspaceControlFailureCausesAndPrivacy(t *testing.T) {
	f := newTopicFx(t)
	dc := workspaceOwnedDesktop(t, f.api.hub, f.pool, topicOwner, "diagnostics")
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET daemon_id=$2 WHERE id=$1`, f.sess, dc.ID); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previous := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer func() { failurelog.Default = previous }()
	for _, tc := range []struct {
		name     string
		session  string
		canceled bool
		want     protocol.WorkspaceControlCause
	}{
		{"invalid session", "canary-secret/private/path", false, protocol.WorkspaceControlInvalid},
		{"missing manifest", f.sess, false, protocol.WorkspaceControlManifest},
		{"unknown session", newUUID(), false, protocol.WorkspaceControlSessionUnknown},
		{"canceled storage", f.sess, true, protocol.WorkspaceControlDeadline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.canceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			req := protocol.LocalWorkspaceControl{SessionID: tc.session, RequestID: newUUID(), Operation: "canary-secret/private/path"}
			reply := f.api.hub.localWorkspaceControl(ctx, f.pool, dc, req)
			if reply.OK || reply.Cause != tc.want {
				t.Fatalf("failure classification: %+v", reply)
			}
			if !strings.Contains(logs.String(), `"cause":"`+string(tc.want)+`"`) {
				t.Fatalf("missing cause: %s", logs.String())
			}
			if strings.Contains(logs.String(), "canary-secret") {
				t.Fatal("untrusted request reached diagnostics")
			}
		})
	}
	before := logs.Len()
	_ = f.api.hub.localWorkspaceControl(t.Context(), f.pool, dc, protocol.LocalWorkspaceControl{SessionID: f.sess, RequestID: newUUID(), Operation: "canary-secret/private/path"})
	if logs.Len() != before {
		t.Fatal("repeated refusal was not rate limited")
	}
}
