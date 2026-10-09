package server

// The broadcast filter must fail CLOSED (spec 8): whatever shape a payload
// takes, a private session's id and data never reach another account's socket.
// These tests need no database: the hub's private-session record is set
// directly.

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const scopeSecretID = "9c0a1b2c-3d4e-4f50-8a6b-7c8d9e0f1a2b"

func newScopeHub(t *testing.T) (hub *Hub, owner, foreign *BrowserConn) {
	t.Helper()
	hub = NewHub()
	hub.notePrivate(scopeSecretID, privAcctA)
	owner = &BrowserConn{ID: "owner", AccountID: privAcctA, send: make(chan []byte, 8)}
	foreign = &BrowserConn{ID: "foreign", AccountID: privAcctB, send: make(chan []byte, 8)}
	hub.RegisterBrowser(owner)
	hub.RegisterBrowser(foreign)
	return hub, owner, foreign
}

// The blocker: a session_state_changed with a string message (every start
// failure) used to fail the scope decode and go to everyone.
func TestPrivateSessionStateChangedWithMessageIsWithheld(t *testing.T) {
	hub, owner, foreign := newScopeHub(t)
	msg := "credential error"
	hub.BroadcastJSON(protocol.SessionStateChanged{
		Type: "session_state_changed", SessionID: scopeSecretID, Status: "error", Message: &msg,
	})
	if len(foreign.send) != 0 {
		t.Fatalf("a foreign account received a private session's error: %s", <-foreign.send)
	}
	if len(owner.send) != 1 || !strings.Contains(string(<-owner.send), "credential error") {
		t.Error("the owner must receive its session's error")
	}
}

// Undecodable-as-expected payloads still fail closed when they name the session.
func TestScopeFailsClosedOnAnyDecodeProblem(t *testing.T) {
	hub, owner, foreign := newScopeHub(t)
	for name, raw := range map[string]string{
		"message is a string":          `{"type":"x","session_id":"` + scopeSecretID + `","message":"boom"}`,
		"message is a number":          `{"type":"x","session_id":"` + scopeSecretID + `","message":7}`,
		"session is a string":          `{"type":"x","session":"` + scopeSecretID + `","session_id":"` + scopeSecretID + `"}`,
		"session id is not a string":   `{"type":"x","session":{"id":5},"message":{"session_id":"` + scopeSecretID + `"}}`,
		"affected is not a list":       `{"type":"x","session_id":"` + scopeSecretID + `","affected_session_ids":"nope"}`,
		"message names it, others bad": `{"type":"x","message":{"session_id":"` + scopeSecretID + `","body":1},"session":[1]}`,
		"trailing garbage":             `{"type":"x","session_id":"` + scopeSecretID + `"} trailing`,
		"nested only":                  `{"type":"x","detail":{"deep":{"session_id":"` + scopeSecretID + `"}}}`,
		// MINOR 39 (b): unexpected nesting and spellings still name the session.
		"session object nested under data": `{"type":"x","data":{"session":{"id":"` + scopeSecretID + `"}}}`,
		"session object in a list":         `{"type":"x","items":[{"session":{"id":"` + scopeSecretID + `"}}]}`,
		"camelCase key":                    `{"type":"x","detail":{"sessionId":"` + scopeSecretID + `"}}`,
		"prefixed snake key":               `{"type":"x","payload":{"parent_session_id":"` + scopeSecretID + `"}}`,
		"prefixed camel key":               `{"type":"x","payload":{"parentSessionId":"` + scopeSecretID + `"}}`,
		"list under a session_id key":      `{"type":"x","payload":{"failed_session_id":["` + scopeSecretID + `"]}}`,
	} {
		hub.BroadcastToBrowsers([]byte(raw))
		if len(foreign.send) != 0 {
			t.Errorf("%s: a foreign account received a private session's payload: %s", name, <-foreign.send)
		}
		if len(owner.send) != 1 {
			t.Errorf("%s: the owner did not receive its payload", name)
		}
		for len(owner.send) > 0 {
			<-owner.send
		}
	}
	// A payload that names no session, JSON or not, is not session-scoped.
	hub.BroadcastToBrowsers([]byte(`{"type":"preview_cleared","message":"hi"}`))
	if len(foreign.send) != 1 {
		t.Error("a payload that names no session must still go to everyone")
	}
}

// protoSessionStructNames lists the protocol structs that name a session: one
// with a session_id / affected_session_ids field, or a SessionInfo /
// MessageInfo field (a nested session or a list of them). It parses the
// package source, so a message added later is picked up here.
func protoSessionStructNames(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir("../protocol")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, "../protocol/"+e.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	var names []string
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, fld := range st.Fields.List {
				tag := ""
				if fld.Tag != nil {
					tag = fld.Tag.Value
				}
				typ := types2str(fld.Type)
				if strings.Contains(tag, `json:"session_id`) || strings.Contains(tag, `json:"affected_session_ids`) ||
					strings.Contains(typ, "SessionInfo") || strings.Contains(typ, "MessageInfo") {
					names = append(names, ts.Name.Name)
					break
				}
			}
			return true
		})
	}
	sort.Strings(names)
	return names
}

func types2str(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return "*" + types2str(x.X)
	case *ast.ArrayType:
		return "[]" + types2str(x.Elt)
	case *ast.SelectorExpr:
		return types2str(x.X) + "." + x.Sel.Name
	}
	return ""
}

// scopeRegistry has one zero value of every protocol struct that names a
// session. TestScopeRegistryIsComplete fails when a new one is missing.
var scopeRegistry = map[string]any{
	"SessionStarted": protocol.SessionStarted{},
	"SessionOutput":  protocol.SessionOutput{}, "SessionStateChanged": protocol.SessionStateChanged{},
	"SessionMetaChanged": protocol.SessionMetaChanged{}, "SessionTitleChanged": protocol.SessionTitleChanged{},
	"SessionEnded": protocol.SessionEnded{}, "SpawnSession": protocol.SpawnSession{},
	"SendInput": protocol.SendInput{}, "KillSession": protocol.KillSession{},
	"InjectNoteReply": protocol.InjectNoteReply{}, "ResizeSession": protocol.ResizeSession{},
	"InitialState": protocol.InitialState{}, "DaemonDisconnected": protocol.DaemonDisconnected{},
	"BrowserSessionStarted": protocol.BrowserSessionStarted{}, "HistoryDone": protocol.HistoryDone{},
	"FocusStolen": protocol.FocusStolen{}, "FocusGranted": protocol.FocusGranted{},
	"SessionScrollback": protocol.SessionScrollback{}, "SessionReadChanged": protocol.SessionReadChanged{},
	"SubscribeSession": protocol.SubscribeSession{}, "UnsubscribeSession": protocol.UnsubscribeSession{},
	"BrowserSendInput":     protocol.BrowserSendInput{},
	"BrowserResizeSession": protocol.BrowserResizeSession{}, "RequestScrollback": protocol.RequestScrollback{},
	"MarkSessionRead": protocol.MarkSessionRead{}, "SetSessionStar": protocol.SetSessionStar{},
	"SessionStarChanged": protocol.SessionStarChanged{}, "MessageInfo": protocol.MessageInfo{},
	"AgentEvent": protocol.AgentEvent{}, "AgentEventAck": protocol.AgentEventAck{},
	"AgentUserMessage": protocol.AgentUserMessage{}, "SetSessionModel": protocol.SetSessionModel{},
	"InterruptSession": protocol.InterruptSession{}, "SubscribeAgentEvents": protocol.SubscribeAgentEvents{},
	"AgentEventsReplayDone": protocol.AgentEventsReplayDone{}, "MessageCreated": protocol.MessageCreated{},
	"MessageAnswered": protocol.MessageAnswered{},
}

func TestScopeRegistryIsComplete(t *testing.T) {
	for _, name := range protoSessionStructNames(t) {
		if _, ok := scopeRegistry[name]; !ok {
			t.Errorf("protocol.%s names a session but is not in scopeRegistry: add it so the privacy filter is tested against it", name)
		}
	}
}

// fillForScope sets every exported, JSON-visible field of v: session-naming
// fields to the secret id, every other string to free text (the shape that
// broke the decoder), everything else to a non-zero value.
func fillForScope(v reflect.Value, structName string) {
	switch v.Kind() {
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fillForScope(v.Elem(), structName)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			tag := strings.Split(f.Tag.Get("json"), ",")[0]
			if tag == "-" {
				continue
			}
			fv := v.Field(i)
			switch {
			case tag == "session_id" && fv.Kind() == reflect.String,
				tag == "id" && structName == "SessionInfo" && fv.Kind() == reflect.String:
				fv.SetString(scopeSecretID)
			case tag == "affected_session_ids":
				fv.Set(reflect.ValueOf([]string{scopeSecretID}))
			default:
				fillForScope(fv, f.Type.Name())
			}
		}
	case reflect.String:
		v.SetString("credential error")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Float64:
		v.SetFloat(1.5)
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 { // json.RawMessage / []byte
			if v.Type() == reflect.TypeOf(json.RawMessage(nil)) {
				v.Set(reflect.ValueOf(json.RawMessage(`{"k":"credential error"}`)))
			}
			return
		}
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fillForScope(s.Index(0), v.Type().Elem().Name())
		v.Set(s)
	}
}

// Every protocol message that names a session, with every optional field set,
// is withheld from a foreign account and delivered to the owner.
func TestPrivateSessionNeverReachesAForeignAccount(t *testing.T) {
	hub, owner, foreign := newScopeHub(t)
	names := make([]string, 0, len(scopeRegistry))
	for n := range scopeRegistry {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			for len(foreign.send) > 0 {
				<-foreign.send
			}
			for len(owner.send) > 0 {
				<-owner.send
			}
			ptr := reflect.New(reflect.TypeOf(scopeRegistry[name]))
			fillForScope(ptr.Elem(), name)
			raw, err := json.Marshal(ptr.Interface())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), scopeSecretID) {
				t.Fatalf("the filled %s does not name the session: %s", name, raw)
			}
			hub.BroadcastToBrowsers(raw)
			if len(foreign.send) != 0 {
				got := string(<-foreign.send)
				// daemon_disconnected is the one message that lists sessions and is still
				// meaningful without the private ones: it is delivered with them removed.
				if name != "DaemonDisconnected" || strings.Contains(got, scopeSecretID) {
					t.Fatalf("a foreign account received a private session's %s: %s", name, got)
				}
			}
			if len(owner.send) != 1 {
				t.Fatalf("the owner did not receive its own %s", name)
			}
			<-owner.send
		})
	}
}

// A failed load is not permanent: the hub stays closed while the database is
// down, retries with a backoff on use, and recovers when it answers, keeping
// what MarkPrivate noted in the meantime.
func TestPrivacyLoadFailureRecovers(t *testing.T) {
	hub := NewHub()
	var calls atomic.Int32
	var healthy atomic.Bool
	hub.privacy.retryFloor, hub.privacy.retryCeil = time.Millisecond, 4*time.Millisecond
	hub.privacy.load = func(context.Context) (map[string]string, error) {
		calls.Add(1)
		if !healthy.Load() {
			return nil, errors.New("database down")
		}
		return map[string]string{scopeSecretID: privAcctA}, nil
	}
	hub.startPrivacyReloader()
	owners, err := hub.privacy.load(context.Background())
	hub.applyPrivacyLoad(owners, err)
	if _, ok := hub.privateOwners([]string{scopeSecretID}); ok {
		t.Fatal("a hub whose load failed must fail closed")
	}
	hub.notePrivate("noted-while-down", privAcctA)
	// Still down: it keeps retrying (with a backoff) and stays closed.
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < 3 && time.Now().Before(deadline) {
		hub.privateOwners(nil)
		time.Sleep(time.Millisecond)
	}
	if calls.Load() < 3 {
		t.Fatalf("the hub did not retry a failed load (%d calls)", calls.Load())
	}
	if hub.accountCanSee(privAcctA, scopeSecretID) {
		t.Fatal("still failed: accountCanSee must be closed")
	}
	// Recovery.
	healthy.Store(true)
	for time.Now().Before(deadline) {
		if got, ok := hub.privateOwners([]string{scopeSecretID, "noted-while-down"}); ok {
			if got[scopeSecretID] != privAcctA || got["noted-while-down"] != privAcctA {
				t.Fatalf("recovered with the wrong record: %v", got)
			}
			if hub.accountCanSee(privAcctB, scopeSecretID) || !hub.accountCanSee(privAcctA, scopeSecretID) {
				t.Error("after recovery accountCanSee must apply the loaded owners")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the hub never recovered after the database came back")
}

// sessionRefsOf reads every spelling of "this payload is about that session" (MINOR 39 b): a
// session_id or *_session_id or *sessionId key at any depth, and the id of any object under a key
// named "session", however deep. Payloads that name nothing name nothing.
func TestSessionRefsOfExtendedShapes(t *testing.T) {
	const id = scopeSecretID
	for name, raw := range map[string]string{
		"nested session object": `{"data":{"session":{"id":"` + id + `"}}}`,
		"deeper session object": `{"a":{"b":[{"session":{"id":"` + id + `","title":"t"}}]}}`,
		"sessionId":             `{"x":{"sessionId":"` + id + `"}}`,
		"X_session_id":          `{"x":{"origin_session_id":"` + id + `"}}`,
		"XSessionId":            `{"x":{"targetSessionId":"` + id + `"}}`,
		"list of ids":           `{"x":{"cancelled_session_id":["` + id + `"]}}`,
	} {
		primary, _ := sessionRefsOf([]byte(raw))
		if !slices.Contains(primary, id) {
			t.Errorf("%s: sessionRefsOf(%s) = %v, want the id", name, raw, primary)
		}
	}
	for name, raw := range map[string]string{
		"nothing":            `{"type":"preview_cleared","message":"hi"}`,
		"a session title":    `{"session":{"title":"not an id"}}`,
		"unrelated id key":   `{"x":{"id":"` + id + `"}}`,
		"word containing it": `{"x":{"sessions_total":3}}`,
	} {
		if primary, _ := sessionRefsOf([]byte(raw)); len(primary) != 0 {
			t.Errorf("%s: sessionRefsOf(%s) = %v, want none", name, raw, primary)
		}
	}
}
