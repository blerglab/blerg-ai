package server

// GET /metrics: the runner's telemetry in the Prometheus text exposition format.
//
// Off unless BLERG_RUNNER_METRICS_TOKEN is set; then a scraper presents it as a bearer token. Everything is
// computed from the same rows as the Insights page (sessions, start_stage / turn_done events, cron runs), over
// ALL history, because Prometheus counters and histograms are cumulative: a sliding window would look like a
// counter reset. They stay monotonic as long as sessions are not deleted. Labels are bounded (runtime, status,
// model, step, a few fixed names): never a session, account or title. The body is cached for a short time so
// a tight scrape interval does not become tight queries.

import (
	"context"
	"crypto/subtle"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

const (
	metricsCacheTTL = 15 * time.Second
	metricsErrTTL   = 5 * time.Second
)

var (
	startBuckets    = []float64{1, 2, 5, 10, 20, 30, 45, 60, 90, 120, 180, 300, 600, 1200}
	lifetimeBuckets = []float64{60, 300, 900, 1800, 3600, 7200, 14400, 28800, 86400, 172800, 604800}
)

// metricsState is the endpoint's token and its short-lived cache.
type metricsState struct {
	token string

	mu      sync.Mutex
	body    string
	builtAt time.Time
	err     error
	errAt   time.Time
}

// SetMetricsToken turns the /metrics endpoint on, with token as the bearer credential a scraper must present.
// An empty token leaves it off (404).
func (a *API) SetMetricsToken(token string) { a.metrics.token = strings.TrimSpace(token) }

// HandleMetrics is GET /metrics.
func (a *API) HandleMetrics(w http.ResponseWriter, r *http.Request) {
	if a.metrics.token == "" {
		http.NotFound(w, r)
		return
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if got == "" || got == r.Header.Get("Authorization") || subtle.ConstantTimeCompare([]byte(got), []byte(a.metrics.token)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="blerg-runner metrics"`)
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	body, err := a.metricsBody(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not compute metrics")
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(body))
}

func (a *API) metricsBody(ctx context.Context) (string, error) {
	m := &a.metrics
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.body != "" && time.Since(m.builtAt) < metricsCacheTTL {
		return m.body, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// A failed build is remembered briefly, so a queue of scrapers does not each repeat a slow failing query.
	if m.err != nil && time.Since(m.errAt) < metricsErrTTL {
		return "", m.err
	}
	body, err := a.renderMetrics(ctx, time.Now().UTC())
	if err != nil {
		m.err, m.errAt = err, time.Now()
		return "", err
	}
	m.err = nil
	m.body, m.builtAt = body, time.Now()
	return body, nil
}

func (a *API) renderMetrics(ctx context.Context, now time.Time) (string, error) {
	w := &promWriter{}
	if a.dbPool == nil {
		w.gauge("blerg_runner_up", "1 when the runner has a database to report from.", []promSample{{value: 0}})
		return w.String(), nil
	}
	from, to := time.Unix(0, 0).UTC(), now.Add(time.Minute)
	sessions, err := db.ListInsightSessions(ctx, a.dbPool, from, to, "")
	if err != nil {
		return "", err
	}
	byID := map[string]db.InsightSession{}
	for _, s := range sessions {
		byID[s.ID] = s
	}
	events, err := db.ListInsightEvents(ctx, a.dbPool, from, to, "", []string{"start_stage"})
	if err != nil {
		return "", err
	}
	tokenRows, err := db.ListTokenUsage(ctx, a.dbPool, from, to, "")
	if err != nil {
		return "", err
	}
	cronRuns, err := db.ListInsightCronRuns(ctx, a.dbPool, from, to, "")
	if err != nil {
		return "", err
	}
	prices, err := db.ListModelPrices(ctx, a.dbPool)
	if err != nil {
		return "", err
	}
	counts, err := db.CountSessionsByStatus(ctx, a.dbPool)
	if err != nil {
		return "", err
	}
	attempts := analyzeAttempts(events, func(id string) string { return byID[id].Runtime })
	markResumes(attempts, byID)

	// sessions now, by runtime and status
	var cur []promSample
	for k, n := range counts {
		cur = append(cur, promSample{labels: []string{"runtime", orUnknown(k[0]), "status", k[1]}, value: float64(n)})
	}
	w.gauge("blerg_runner_sessions", "Agent sessions now, by runtime and status.", cur)

	// sessions started and ended, by runtime
	started := map[string]float64{}
	var lifetimes = map[string][]float64{}
	for _, s := range sessions {
		if s.Kind != "agent" {
			continue
		}
		started[orUnknown(s.Runtime)]++
		if s.EndedAt != nil {
			if d := s.EndedAt.Sub(s.StartedAt).Seconds(); d >= 0 { // clock skew can invert a lifetime
				lifetimes[orUnknown(s.Runtime)] = append(lifetimes[orUnknown(s.Runtime)], d)
			}
		}
	}
	var st []promSample
	for rt, n := range started {
		st = append(st, promSample{labels: []string{"runtime", rt}, value: n})
	}
	w.counter("blerg_runner_sessions_started_total", "Agent sessions started, by runtime.", st)
	var lt []promHist
	for rt, v := range lifetimes {
		lt = append(lt, promHist{labels: []string{"runtime", rt}, values: v})
	}
	w.histogram("blerg_runner_session_lifetime_seconds", "How long ended agent sessions lived.", lifetimeBuckets, lt)

	// starts
	type rk struct{ runtime, kind string }
	startTimes := map[rk][]float64{}
	results := map[[3]string]float64{}
	stepTimes := map[[2]string][]float64{}
	failures := map[[2]string]float64{}
	for _, at := range attempts {
		kind := "start"
		if at.Resume {
			kind = "resume"
		}
		rt := orUnknown(at.Runtime)
		switch {
		case at.Ok:
			results[[3]string{rt, kind, "ready"}]++
			startTimes[rk{rt, kind}] = append(startTimes[rk{rt, kind}], at.Seconds())
			for step, secs := range at.Steps {
				stepTimes[[2]string{rt, step}] = append(stepTimes[[2]string{rt, step}], secs)
			}
		case at.FailedStage != "":
			results[[3]string{rt, kind, "failed"}]++
			failures[[2]string{rt, at.FailedStage}]++
		default:
			results[[3]string{rt, kind, "incomplete"}]++
		}
	}
	var rs []promSample
	for k, n := range results {
		rs = append(rs, promSample{labels: []string{"runtime", k[0], "kind", k[1], "result", k[2]}, value: n})
	}
	w.counter("blerg_runner_start_attempts_total", "Session start attempts (a fresh start or a resume), by result.", rs)
	var sh []promHist
	for k, v := range startTimes {
		sh = append(sh, promHist{labels: []string{"runtime", k.runtime, "kind", k.kind}, values: v})
	}
	w.histogram("blerg_runner_start_duration_seconds", "Time from the start of an attempt until the agent was ready.", startBuckets, sh)
	var sp []promHist
	for k, v := range stepTimes {
		sp = append(sp, promHist{labels: []string{"runtime", k[0], "step", k[1]}, values: v})
	}
	w.histogram("blerg_runner_start_step_duration_seconds", "Time spent in each step of a successful start.", startBuckets, sp)
	var fs []promSample
	for k, n := range failures {
		fs = append(fs, promSample{labels: []string{"runtime", k[0], "stage", k[1]}, value: n})
	}
	w.counter("blerg_runner_start_failures_total", "Start attempts that failed, by the stage that failed.", fs)

	// tokens
	pm := priceIndex(prices)
	type tot struct{ turns, in, out, cr, cw int64 }
	byModel := map[string]*tot{}
	for _, r := range tokenRows {
		m := orUnknown(r.Model)
		t := byModel[m]
		if t == nil {
			t = &tot{}
			byModel[m] = t
		}
		t.turns += r.Turns
		t.in += r.Input
		t.out += r.Output
		t.cr += r.CacheRead
		t.cw += r.CacheWrite
	}
	var turns, toks, cost []promSample
	for m, t := range byModel {
		turns = append(turns, promSample{labels: []string{"model", m}, value: float64(t.turns)})
		toks = append(toks,
			promSample{labels: []string{"model", m, "type", "input"}, value: float64(t.in)},
			promSample{labels: []string{"model", m, "type", "output"}, value: float64(t.out)},
			promSample{labels: []string{"model", m, "type", "cache_read"}, value: float64(t.cr)},
			promSample{labels: []string{"model", m, "type", "cache_write"}, value: float64(t.cw)})
		if p, ok := pm[m]; ok {
			cost = append(cost, promSample{labels: []string{"model", m}, value: costUSD(t.in, t.out, t.cr, t.cw, p)})
		}
	}
	w.counter("blerg_runner_turns_total", "Agent turns completed, by model.", turns)
	w.counter("blerg_runner_tokens_total", "Tokens used, by model and type.", toks)
	w.counter("blerg_runner_cost_usd_total", "Estimated cost in US dollars at the configured per-model prices (models without a price are left out).", cost)

	// cluster
	if jm := a.hub.JobManager(); jm != nil {
		cs := jm.Status()
		w.gauge("blerg_runner_cluster_sessions_active", "Cluster sessions in use.", []promSample{{value: float64(cs.ActiveSessions)}})
		w.gauge("blerg_runner_cluster_sessions_max", "The cluster session cap.", []promSample{{value: float64(cs.MaxSessions)}})
	}

	// crons
	statuses := map[string]float64{}
	var cronSecs []float64
	for _, r := range cronRuns {
		statuses[r.Status]++
		if r.StartedAt != nil && r.EndedAt != nil {
			if d := r.EndedAt.Sub(*r.StartedAt).Seconds(); d >= 0 {
				cronSecs = append(cronSecs, d)
			}
		}
	}
	var cr []promSample
	for s, n := range statuses {
		cr = append(cr, promSample{labels: []string{"status", s}, value: n})
	}
	w.counter("blerg_runner_cron_runs_total", "Cron firings, by status.", cr)
	w.histogram("blerg_runner_cron_run_duration_seconds", "How long a cron's session lived.", lifetimeBuckets, []promHist{{values: cronSecs}})
	return w.String(), nil
}

// ─── the exposition writer ────────────────────────────────────────────────────

type promSample struct {
	labels []string // name, value, name, value ...
	value  float64
}

type promHist struct {
	labels []string
	values []float64
}

type promWriter struct{ sb strings.Builder }

func (w *promWriter) String() string { return w.sb.String() }

func (w *promWriter) meta(name, help, typ string) {
	w.sb.WriteString("# HELP " + name + " " + strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(help) + "\n")
	w.sb.WriteString("# TYPE " + name + " " + typ + "\n")
}

func (w *promWriter) gauge(name, help string, s []promSample)   { w.samples(name, help, "gauge", s) }
func (w *promWriter) counter(name, help string, s []promSample) { w.samples(name, help, "counter", s) }

func (w *promWriter) samples(name, help, typ string, s []promSample) {
	if len(s) == 0 {
		return
	}
	w.meta(name, help, typ)
	sort.Slice(s, func(i, j int) bool { return strings.Join(s[i].labels, "\x00") < strings.Join(s[j].labels, "\x00") })
	for _, x := range s {
		w.sb.WriteString(name + promLabels(x.labels) + " " + promFloat(x.value) + "\n")
	}
}

// histogram writes cumulative buckets, sum and count for each label set. A label set with no values is still
// written (all zero) when it is the only one, so the series exists before the first observation.
func (w *promWriter) histogram(name, help string, buckets []float64, hs []promHist) {
	if len(hs) == 0 {
		return
	}
	w.meta(name, help, "histogram")
	sort.Slice(hs, func(i, j int) bool { return strings.Join(hs[i].labels, "\x00") < strings.Join(hs[j].labels, "\x00") })
	for _, h := range hs {
		counts := make([]float64, len(buckets))
		sum := 0.0
		for _, v := range h.values {
			sum += v
			for i, b := range buckets {
				if v <= b {
					counts[i]++
				}
			}
		}
		for i, b := range buckets {
			w.sb.WriteString(name + "_bucket" + promLabels(append(append([]string{}, h.labels...), "le", promFloat(b))) + " " + promFloat(counts[i]) + "\n")
		}
		w.sb.WriteString(name + "_bucket" + promLabels(append(append([]string{}, h.labels...), "le", "+Inf")) + " " + promFloat(float64(len(h.values))) + "\n")
		w.sb.WriteString(name + "_sum" + promLabels(h.labels) + " " + promFloat(sum) + "\n")
		w.sb.WriteString(name + "_count" + promLabels(h.labels) + " " + promFloat(float64(len(h.values))) + "\n")
	}
}

func promLabels(kv []string) string {
	if len(kv) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteByte('{')
	for i := 0; i+1 < len(kv); i += 2 {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(kv[i] + `="` + promEscape(kv[i+1]) + `"`)
	}
	sb.WriteByte('}')
	return sb.String()
}

func promEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func promFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
