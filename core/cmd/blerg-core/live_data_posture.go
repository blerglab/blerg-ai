package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/livedata"
)

func liveDataPosture(deps api.Deps) func(context.Context) (bool, error) {
	return func(parent context.Context) (bool, error) {
		// Managed desktop agents have the Docker group. A reported safe host can
		// never override this installation policy or the epoch-bound Settings consent.
		if deps.LiveDataDesktop {
			return false, nil
		}
		fail := func() (bool, error) {
			failurelog.Default.Event("live_data_posture", slog.LevelWarn, "cause", "runner_posture_unavailable")
			return false, livedata.ErrUnavailable
		}
		if deps.LiveDataRunnerClient == nil || deps.LiveDataRunnerClient.Transport == nil || deps.LiveDataRunnerURL == "" || deps.InternalKey == "" {
			return fail()
		}
		ctx, cancel := context.WithTimeout(parent, 3*time.Second)
		defer cancel()
		request, e := http.NewRequestWithContext(ctx, "POST", deps.LiveDataRunnerURL+"/internal/live-data/posture", nil) // #nosec G704 -- fixed callback on the configured dedicated Runner, never request-selected.
		if e != nil {
			return fail()
		}
		request.Header.Set("X-Internal-Key", deps.InternalKey)
		client := *deps.LiveDataRunnerClient
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		start := time.Now()
		response, e := client.Do(request) // #nosec G704 -- dedicated Runner mTLS client; redirects are disabled.
		if e != nil {
			return fail()
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != 200 {
			return fail()
		}
		raw, e := io.ReadAll(io.LimitReader(response.Body, 2049))
		if e != nil || len(raw) > 2048 {
			return fail()
		}
		var report ld.PostureReport
		// Strict decoding prevents an added response field from silently changing
		// the authority semantics; the report is a deliberately tiny contract.
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&report) != nil || !report.Valid() {
			return fail()
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return fail()
		}
		end := time.Now()
		rtt := end.Sub(start)
		offset := report.CheckedAt.Sub(start.Add(rtt / 2))
		if offset < 0 {
			offset = -offset
		}
		if rtt < 0 || rtt > 3*time.Second || offset+rtt/2 > 30*time.Second {
			return fail()
		}
		return report.Safe, nil
	}
}
