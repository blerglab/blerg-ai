package mcpgw

// Proposal fixes (round 2): the definite/unknown classification of a failed tools/call, NUL
// handling, the review link, "nothing was sent" and the per-session cap.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// toolsCallWrapper points the harness at a server that forwards everything to the fake upstream
// except tools/call, which it answers with answer (and counts).
func toolsCallWrapper(t *testing.T, h *harness, answer func(w http.ResponseWriter, id json.RawMessage)) (call func() ApprovedCall, delivered *int) {
	t.Helper()
	n := new(int)
	wrapper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"tools/call"`)) {
			*n++
			var env struct {
				ID json.RawMessage `json:"id"`
			}
			_ = json.Unmarshal(body, &env)
			answer(w, env.ID)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		h.up.handle(w, r)
	}))
	t.Cleanup(wrapper.Close)
	h.core.url = wrapper.URL + "/mcp"
	return func() ApprovedCall {
		c := approved(h, "send", `{}`)
		c.URLSnapshot = wrapper.URL + "/mcp"
		return c
	}, n
}

func TestClassifyHTTPStatusAfterSending(t *testing.T) {
	// A definite failure is only a 4xx other than 408/429: the request was rejected before it
	// acted. Everything else may have arrived after the side effect.
	cases := []struct {
		status  int
		unknown bool
	}{
		{400, false}, {401, false}, {403, false}, {404, false}, {405, false}, {409, false}, {413, false}, {415, false}, {422, false}, {451, false},
		{408, true}, {429, true},
		{500, true}, {501, true}, {502, true}, {503, true}, {504, true}, {507, true}, {520, true},
		{301, true}, {302, true}, {307, true},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			h := newHarness(t, nil)
			call, _ := toolsCallWrapper(t, h, func(w http.ResponseWriter, _ json.RawMessage) { w.WriteHeader(tc.status) })
			_, err := h.gw.ExecuteApproved(context.Background(), call())
			if err == nil {
				t.Fatal("want an error")
			}
			if got := errors.Is(err, ErrOutcomeUnknown); got != tc.unknown {
				t.Errorf("status %d: unknown=%v, want %v (err %v)", tc.status, got, tc.unknown, err)
			}
			var ae *ApprovedError
			if !errors.As(err, &ae) || ae.NotSent {
				t.Errorf("a status after the call was sent must not claim nothing was sent: %+v", ae)
			}
		})
	}
}

func TestClassifyJSONRPCErrorsAfterSending(t *testing.T) {
	cases := []struct {
		code    int
		unknown bool
	}{
		{-32600, false}, {-32601, false}, {-32602, false}, {-32700, false},
		{-32603, true}, {-32000, true}, {-32001, true}, {-32099, true}, {0, true}, {1, true}, {500, true},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.code), func(t *testing.T) {
			h := newHarness(t, nil)
			call, _ := toolsCallWrapper(t, h, func(w http.ResponseWriter, id json.RawMessage) {
				w.Header().Set("Content-Type", "application/json")
				b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": tc.code, "message": "boom"}})
				_, _ = w.Write(b)
			})
			_, err := h.gw.ExecuteApproved(context.Background(), call())
			if err == nil {
				t.Fatal("want an error")
			}
			if got := errors.Is(err, ErrOutcomeUnknown); got != tc.unknown {
				t.Errorf("code %d: unknown=%v, want %v (err %v)", tc.code, got, tc.unknown, err)
			}
		})
	}
}

func TestClassifyUnparseableReplyAfterSendingIsUnknown(t *testing.T) {
	for name, answer := range map[string]func(http.ResponseWriter, json.RawMessage){
		"not json": func(w http.ResponseWriter, _ json.RawMessage) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("<html>oops"))
		},
		"wrong id": func(w http.ResponseWriter, _ json.RawMessage) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":99999,"result":{}}`))
		},
		"empty stream": func(w http.ResponseWriter, _ json.RawMessage) {
			w.Header().Set("Content-Type", "text/event-stream")
		},
		"unknown content type": func(w http.ResponseWriter, _ json.RawMessage) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("ok"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, nil)
			call, _ := toolsCallWrapper(t, h, answer)
			_, err := h.gw.ExecuteApproved(context.Background(), call())
			if !errors.Is(err, ErrOutcomeUnknown) {
				t.Errorf("err = %v, want ErrOutcomeUnknown", err)
			}
		})
	}
}

func TestExecuteApprovedNothingSentIsFlagged(t *testing.T) {
	notSent := func(t *testing.T, err error, want bool) {
		t.Helper()
		var ae *ApprovedError
		if !errors.As(err, &ae) {
			t.Fatalf("err = %v, want an ApprovedError", err)
		}
		if ae.NotSent != want {
			t.Errorf("NotSent = %v, want %v (%s)", ae.NotSent, want, ae.Msg)
		}
	}
	t.Run("credential fetch fails", func(t *testing.T) {
		h := newHarness(t, nil)
		h.core.err = errors.New("core is down")
		_, err := h.gw.ExecuteApproved(context.Background(), approved(h, "send", `{}`))
		notSent(t, err, true)
	})
	t.Run("no live sign-in", func(t *testing.T) {
		h := newHarness(t, nil)
		c := approved(h, "send", `{}`)
		c.Proof = Proof{AccountID: "acct-1"}
		_, err := h.gw.ExecuteApproved(context.Background(), c)
		notSent(t, err, true)
	})
	t.Run("tools/list fails", func(t *testing.T) {
		h := newHarness(t, nil)
		h.up.mu.Lock()
		h.up.status = http.StatusInternalServerError
		h.up.mu.Unlock()
		_, err := h.gw.ExecuteApproved(context.Background(), approved(h, "send", `{}`))
		notSent(t, err, true)
	})
	t.Run("tool changed", func(t *testing.T) {
		h := newHarness(t, nil)
		c := approved(h, "send", `{}`)
		h.up.setDescription("send", "different")
		_, err := h.gw.ExecuteApproved(context.Background(), c)
		notSent(t, err, true)
	})
	t.Run("address changed", func(t *testing.T) {
		h := newHarness(t, nil)
		c := approved(h, "send", `{}`)
		c.URLSnapshot += "?x=1"
		_, err := h.gw.ExecuteApproved(context.Background(), c)
		notSent(t, err, true)
	})
	t.Run("connection deleted is final", func(t *testing.T) {
		h := newHarness(t, nil)
		h.core.err = ErrConnectionGone
		_, err := h.gw.ExecuteApproved(context.Background(), approved(h, "send", `{}`))
		notSent(t, err, false)
	})
	t.Run("connect refused on the call", func(t *testing.T) {
		h := newHarness(t, nil)
		dead := httptest.NewServer(http.NotFoundHandler())
		u := dead.URL + "/mcp"
		dead.Close()
		h.core.url = u
		c := approved(h, "send", `{}`)
		c.URLSnapshot = u
		_, err := h.gw.ExecuteApproved(context.Background(), c)
		notSent(t, err, true)
	})
	t.Run("a 400 answer was sent", func(t *testing.T) {
		h := newHarness(t, nil)
		call, _ := toolsCallWrapper(t, h, func(w http.ResponseWriter, _ json.RawMessage) { w.WriteHeader(400) })
		_, err := h.gw.ExecuteApproved(context.Background(), call())
		notSent(t, err, false)
	})
}

func TestSanitizeResultStripsNULInEveryTextPosition(t *testing.T) {
	raw := json.RawMessage(`{"content":[
		{"type":"text","text":"\u0000first"},
		{"type":"text","text":"mid\u0000dle"},
		{"type":"text","text":"last\u0000"},
		{"type":"image","data":"AAAA\u0000","text":"\u0000"},
		{"type":"te\u0000xt","text":"x\u0000y"},
		{"type":"text","text":"\u0000\u0000"}
	],"isError":false,"structuredContent":{"k":"\u0000"}}`)
	out := sanitizeResult(raw, 10000)
	if bytes.Contains(out, []byte(`\u0000`)) || bytes.ContainsRune(out, 0) {
		t.Fatalf("NUL survived: %s", out)
	}
	var r struct {
		Content []struct{ Text string } `json:"content"`
	}
	if err := json.Unmarshal(out, &r); err != nil || len(r.Content) != 6 {
		t.Fatalf("result = %s (%v)", out, err)
	}
	if r.Content[0].Text != "first" || r.Content[1].Text != "middle" || r.Content[2].Text != "last" || r.Content[4].Text != "[unsupported content omitted]" {
		t.Errorf("texts = %+v", r.Content)
	}
	// A backslash followed by the letters u0000 is not a NUL and is kept.
	keep := sanitizeResult(json.RawMessage(`{"content":[{"type":"text","text":"a\\u0000b"}]}`), 100)
	if !strings.Contains(string(keep), `a\\u0000b`) {
		t.Errorf("a literal backslash-u0000 text was altered: %s", keep)
	}
}

func TestExecuteApprovedNULInErrorTextIsStripped(t *testing.T) {
	h := newHarness(t, nil)
	call, _ := toolsCallWrapper(t, h, func(w http.ResponseWriter, id json.RawMessage) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(id) + `,"error":{"code":-32602,"message":"bad\u0000thing"}}`))
	})
	_, err := h.gw.ExecuteApproved(context.Background(), call())
	var ae *ApprovedError
	if !errors.As(err, &ae) || strings.ContainsRune(ae.Msg, 0) || !strings.Contains(ae.Msg, "badthing") {
		t.Fatalf("err = %v", err)
	}
}

func TestProposeLinkIsTheProposalsPageWithAnIDQuery(t *testing.T) {
	h := newHarness(t, withSink(50, testRunnerURL))
	gh := h.grant("alpha", map[string]string{"send": "propose"}, 0)
	r := h.callTool(gh, "send", map[string]any{"n": 1})
	p := proposalRows(t, h)[0]
	if text := resultText(r); !strings.Contains(text, testRunnerURL+"/proposals?id="+p.ID) || strings.Contains(text, "/proposals/"+p.ID) {
		t.Errorf("link = %q", text)
	}
}

func TestProposePerSessionCap(t *testing.T) {
	h := newHarness(t, withSink(50, testRunnerURL))
	gh := h.grant("alpha", map[string]string{"send": "propose"}, 0)
	for i := range DefaultMaxPendingPerSession {
		if r := h.callTool(gh, "send", map[string]any{"n": i}); r.result()["isError"] == true {
			t.Fatalf("proposal %d refused too early: %s", i, r.raw)
		}
	}
	r := h.callTool(gh, "send", map[string]any{"n": 99})
	if r.result()["isError"] != true || !strings.Contains(resultText(r), "session") || !strings.Contains(resultText(r), "Do not retry") {
		t.Fatalf("over the session cap the agent needs a clear refusal: %s", r.raw)
	}
	if n := len(proposalRows(t, h)); n != DefaultMaxPendingPerSession {
		t.Errorf("%d stored, want %d", n, DefaultMaxPendingPerSession)
	}
}
