package server

// Insights: GET /api/insights (and the price table behind its dollar estimates).
//
// Who sees what. An administrator (the account.manage capability) sees every session; everyone else sees only
// the sessions they started. An administrator can ask for ?scope=mine to look at their own. Private sessions
// (every cron session, and any session with MCP connections) are visible to their owner alone on every
// surface, so for anyone else they never appear BY NAME: the lists (slowest starts, top token users) skip
// them. Their counts, durations and token totals do go into an administrator's aggregates, as a total with no
// session, title or content attached, because leaving the crons out would leave out most of what a person
// asked to see. docs/telemetry.md says so.

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

const (
	insightsAdminCap = "account.manage"
	insightsTopN     = 10
)

var insightsRanges = map[string]time.Duration{
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
	"90d": 90 * 24 * time.Hour,
}

type countRow struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

type insightsRange struct {
	Label string    `json:"label"`
	From  time.Time `json:"from"`
	To    time.Time `json:"to"`
}

type insightsSessions struct {
	Started        int         `json:"started"`
	Ended          int         `json:"ended"`
	Alive          int         `json:"alive"`
	ByRuntime      []countRow  `json:"by_runtime"`
	ByOutcome      []countRow  `json:"by_outcome"`
	LifetimeSecs   quantiles   `json:"lifetime_seconds"`
	RunningSeconds float64     `json:"running_seconds"`
	WaitingSeconds float64     `json:"waiting_seconds"`
	IdleSeconds    float64     `json:"idle_seconds"`
	BusyFraction   float64     `json:"busy_fraction"`
	ByDay          []dayCounts `json:"by_day"`
}

type dayCounts struct {
	Day     string `json:"day"`
	Started int    `json:"started"`
}

type startGroup struct {
	Runtime  string    `json:"runtime"`
	Kind     string    `json:"kind"` // "start" | "resume"
	Attempts int       `json:"attempts"`
	Failed   int       `json:"failed"`
	Seconds  quantiles `json:"seconds"`
}

type stepRow struct {
	Runtime string  `json:"runtime"`
	Step    string  `json:"step"`
	Count   int     `json:"count"`
	P50     float64 `json:"p50"`
	P90     float64 `json:"p90"`
}

type slowRow struct {
	SessionID string    `json:"session_id"`
	Title     string    `json:"title"`
	Runtime   string    `json:"runtime"`
	Seconds   float64   `json:"seconds"`
	At        time.Time `json:"at"`
}

type insightsStartup struct {
	Groups   []startGroup `json:"groups"`
	Steps    []stepRow    `json:"steps"`
	Failures []countRow   `json:"failures"`
	Slowest  []slowRow    `json:"slowest"`
}

type tokenCounts struct {
	Turns      int64    `json:"turns"`
	Input      int64    `json:"input"`
	Output     int64    `json:"output"`
	CacheRead  int64    `json:"cache_read"`
	CacheWrite int64    `json:"cache_write"`
	CostUSD    *float64 `json:"cost_usd"`
}

type modelRow struct {
	Model string `json:"model"`
	tokenCounts
	Priced bool `json:"priced"`
}

type dayRow struct {
	Day string `json:"day"`
	tokenCounts
}

type topSession struct {
	SessionID string    `json:"session_id"`
	Title     string    `json:"title"`
	Runtime   string    `json:"runtime"`
	StartedAt time.Time `json:"started_at"`
	tokenCounts
}

type insightsTokens struct {
	Totals         tokenCounts  `json:"totals"`
	CacheHitRate   float64      `json:"cache_hit_rate"`
	ByModel        []modelRow   `json:"by_model"`
	Daily          []dayRow     `json:"daily"`
	TopSessions    []topSession `json:"top_sessions"`
	UnpricedModels []string     `json:"unpriced_models"`
}

type insightsCluster struct {
	Active         int `json:"active"`
	Max            int `json:"max"`
	PeakConcurrent int `json:"peak_concurrent"`
}

type insightsCrons struct {
	Runs    int        `json:"runs"`
	Late    int        `json:"late"`
	Manual  int        `json:"manual"`
	Status  []countRow `json:"by_status"`
	Seconds quantiles  `json:"seconds"`
}

type insightsResponse struct {
	Range       insightsRange    `json:"range"`
	Scope       string           `json:"scope"` // "all" | "mine"
	IsAdmin     bool             `json:"is_admin"`
	GeneratedAt time.Time        `json:"generated_at"`
	Sessions    insightsSessions `json:"sessions"`
	Startup     insightsStartup  `json:"startup"`
	Tokens      insightsTokens   `json:"tokens"`
	Cluster     *insightsCluster `json:"cluster"`
	Crons       insightsCrons    `json:"crons"`
}

// HandleGetInsights is GET /api/insights?range=24h|7d|30d|90d&scope=mine.
func (a *API) HandleGetInsights(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusConflict, "insights need the database")
		return
	}
	label := r.URL.Query().Get("range")
	if label == "" {
		label = "7d"
	}
	span, ok := insightsRanges[label]
	if !ok {
		writeError(w, http.StatusBadRequest, "range must be 24h, 7d, 30d or 90d")
		return
	}
	isAdmin := p.Has(insightsAdminCap)
	owner := p.Sub
	scope := "mine"
	if isAdmin && r.URL.Query().Get("scope") != "mine" {
		owner, scope = "", "all"
	}
	if owner == "" && scope == "mine" { // a token with no subject cannot have "its own" sessions
		writeError(w, http.StatusForbidden, "no account")
		return
	}
	now := time.Now().UTC()
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	resp, err := a.buildInsights(ctx, now, now.Add(-span), now.Add(time.Minute), owner, p.Sub)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not compute insights")
		return
	}
	resp.Range.Label = label
	resp.Scope, resp.IsAdmin = scope, isAdmin
	writeJSON(w, http.StatusOK, resp)
}

// buildInsights computes the whole report for [from, to). owner "" = every session; viewer is the account
// asking, whose own private sessions stay visible by name.
func (a *API) buildInsights(ctx context.Context, now, from, to time.Time, owner, viewer string) (*insightsResponse, error) {
	sessions, err := db.ListInsightSessions(ctx, a.dbPool, from, to, owner)
	if err != nil {
		return nil, err
	}
	agent := make([]db.InsightSession, 0, len(sessions))
	byID := map[string]db.InsightSession{}
	for _, s := range sessions {
		if s.Kind == "agent" {
			agent = append(agent, s)
			byID[s.ID] = s
		}
	}
	events, err := db.ListInsightEvents(ctx, a.dbPool, from, to, owner, []string{"start_stage", "status_changed"})
	if err != nil {
		return nil, err
	}
	tokenRows, err := db.ListTokenUsage(ctx, a.dbPool, from, to, owner)
	if err != nil {
		return nil, err
	}
	cronRuns, err := db.ListInsightCronRuns(ctx, a.dbPool, from, to, owner)
	if err != nil {
		return nil, err
	}
	prices, err := db.ListModelPrices(ctx, a.dbPool)
	if err != nil {
		return nil, err
	}
	// Sessions named in a list may have started before the range: look them up so a private one is recognised.
	needed := map[string]bool{}
	attempts := analyzeAttempts(events, func(id string) string { return byID[id].Runtime })
	for _, at := range attempts {
		if _, ok := byID[at.SessionID]; !ok {
			needed[at.SessionID] = true
		}
	}
	for _, tr := range tokenRows {
		if _, ok := byID[tr.SessionID]; !ok {
			needed[tr.SessionID] = true
		}
	}
	if len(needed) > 0 {
		ids := make([]string, 0, len(needed))
		for id := range needed {
			ids = append(ids, id)
		}
		extra, err := db.GetInsightSessions(ctx, a.dbPool, ids)
		if err != nil {
			return nil, err
		}
		for _, s := range extra {
			byID[s.ID] = s
		}
	}
	markResumes(attempts, byID)
	visible := func(id string) bool {
		s, ok := byID[id]
		return ok && (!s.Private || s.Account == viewer)
	}

	resp := &insightsResponse{GeneratedAt: now, Range: insightsRange{From: from, To: now}}
	resp.Sessions = summariseSessions(agent, events, byID, from, now, to)
	resp.Startup = summariseStartup(attempts, byID, visible)
	resp.Tokens = summariseTokens(tokenRows, byID, prices, visible)
	resp.Crons = summariseCrons(cronRuns, now)
	if jm := a.hub.JobManager(); jm != nil {
		st := jm.Status() //nolint:contextcheck // bounded by the JobManager client timeout and deliberately not tied to the caller, like the status endpoint
		resp.Cluster = &insightsCluster{
			Active: st.ActiveSessions, Max: st.MaxSessions,
			PeakConcurrent: peakConcurrent(agent, "cluster", from, to, now),
		}
	}
	return resp, nil
}

func sortedCounts(m map[string]int) []countRow {
	out := make([]countRow, 0, len(m))
	for k, v := range m {
		out = append(out, countRow{Key: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func isTerminalStatus(s string) bool {
	switch s {
	case "stopped", "error", "ended":
		return true
	}
	return false
}

func summariseSessions(sessions []db.InsightSession, events []db.InsightEvent, byID map[string]db.InsightSession, from, now, to time.Time) insightsSessions {
	out := insightsSessions{ByRuntime: []countRow{}, ByOutcome: []countRow{}, ByDay: []dayCounts{}}
	runtimes, outcomes, days := map[string]int{}, map[string]int{}, map[string]int{}
	var lifetimes []float64
	for _, s := range sessions {
		if !s.StartedAt.Before(from) && s.StartedAt.Before(to) {
			out.Started++
			runtimes[orUnknown(s.Runtime)]++
			days[s.StartedAt.UTC().Format("2006-01-02")]++
		}
		if !isTerminalStatus(s.Status) && s.Status != "disconnected" {
			out.Alive++
		}
		if s.EndedAt != nil && !s.EndedAt.Before(from) && s.EndedAt.Before(to) {
			out.Ended++
			outcome := s.EndReason
			if outcome == "" {
				outcome = s.Status
			}
			outcomes[orUnknown(outcome)]++
			if d := s.EndedAt.Sub(s.StartedAt).Seconds(); d >= 0 { // clock skew can invert a lifetime
				lifetimes = append(lifetimes, d)
			}
		}
	}
	out.ByRuntime, out.ByOutcome = sortedCounts(runtimes), sortedCounts(outcomes)
	out.LifetimeSecs = quantilesOf(lifetimes)
	out.RunningSeconds, out.WaitingSeconds, out.IdleSeconds = statusSeconds(events, byID, from, to, now)
	if total := out.RunningSeconds + out.WaitingSeconds + out.IdleSeconds; total > 0 {
		out.BusyFraction = out.RunningSeconds / total
	}
	for d, n := range days {
		out.ByDay = append(out.ByDay, dayCounts{Day: d, Started: n})
	}
	sort.Slice(out.ByDay, func(i, j int) bool { return out.ByDay[i].Day < out.ByDay[j].Day })
	return out
}

func summariseStartup(attempts []startRecord, byID map[string]db.InsightSession, visible func(string) bool) insightsStartup {
	out := insightsStartup{Groups: []startGroup{}, Steps: []stepRow{}, Failures: []countRow{}, Slowest: []slowRow{}}
	type key struct{ runtime, kind string }
	groups := map[key]*startGroup{}
	times := map[key][]float64{}
	steps := map[[2]string][]float64{}
	fails := map[string]int{}
	for _, at := range attempts {
		kind := "start"
		if at.Resume {
			kind = "resume"
		}
		k := key{orUnknown(at.Runtime), kind}
		g := groups[k]
		if g == nil {
			g = &startGroup{Runtime: k.runtime, Kind: k.kind}
			groups[k] = g
		}
		g.Attempts++
		switch {
		case at.Ok:
			times[k] = append(times[k], at.Seconds())
			for step, secs := range at.Steps {
				sk := [2]string{k.runtime, step}
				steps[sk] = append(steps[sk], secs)
			}
		case at.FailedStage != "":
			g.Failed++
			fails[at.FailedStage]++
		}
	}
	for k, g := range groups {
		g.Seconds = quantilesOf(times[k])
		out.Groups = append(out.Groups, *g)
	}
	sort.Slice(out.Groups, func(i, j int) bool {
		if out.Groups[i].Runtime != out.Groups[j].Runtime {
			return out.Groups[i].Runtime < out.Groups[j].Runtime
		}
		return out.Groups[i].Kind < out.Groups[j].Kind
	})
	for sk, v := range steps {
		q := quantilesOf(v)
		out.Steps = append(out.Steps, stepRow{Runtime: sk[0], Step: sk[1], Count: q.Count, P50: q.P50, P90: q.P90})
	}
	sort.Slice(out.Steps, func(i, j int) bool {
		if out.Steps[i].Runtime != out.Steps[j].Runtime {
			return out.Steps[i].Runtime < out.Steps[j].Runtime
		}
		return stepOrder(out.Steps[i].Step) < stepOrder(out.Steps[j].Step)
	})
	out.Failures = sortedCounts(fails)
	for _, at := range slowestStarts(attempts, insightsTopN, visible) {
		s := byID[at.SessionID]
		out.Slowest = append(out.Slowest, slowRow{SessionID: at.SessionID, Title: s.Title, Runtime: orUnknown(at.Runtime), Seconds: at.Seconds(), At: at.Start})
	}
	return out
}

// stepOrder keeps the stages in the order a start goes through them.
func stepOrder(step string) int {
	for i, s := range []string{"queued", "schedule", "image", "connect", "clone", "plugins", "engine"} {
		if s == step {
			return i
		}
	}
	return 100
}

func priceIndex(prices []db.ModelPrice) map[string]db.ModelPrice {
	m := make(map[string]db.ModelPrice, len(prices))
	for _, p := range prices {
		m[p.Model] = p
	}
	return m
}

func addTokens(c *tokenCounts, r db.TokenRow) {
	c.Turns += r.Turns
	c.Input += r.Input
	c.Output += r.Output
	c.CacheRead += r.CacheRead
	c.CacheWrite += r.CacheWrite
}

func addCost(c *tokenCounts, usd float64) {
	if c.CostUSD == nil {
		z := 0.0
		c.CostUSD = &z
	}
	*c.CostUSD += usd
}

func roundUSD(v float64) float64 { return math.Round(v*10000) / 10000 }

func summariseTokens(rows []db.TokenRow, byID map[string]db.InsightSession, prices []db.ModelPrice, visible func(string) bool) insightsTokens {
	out := insightsTokens{ByModel: []modelRow{}, Daily: []dayRow{}, TopSessions: []topSession{}, UnpricedModels: []string{}}
	pm := priceIndex(prices)
	models := map[string]*modelRow{}
	days := map[string]*dayRow{}
	sessionsTok := map[string]*topSession{}
	unpriced := map[string]bool{}
	for _, r := range rows {
		model := orUnknown(r.Model)
		price, priced := pm[r.Model]
		cost := 0.0
		if priced {
			cost = costUSD(r.Input, r.Output, r.CacheRead, r.CacheWrite, price)
		} else {
			unpriced[model] = true
		}
		addTokens(&out.Totals, r)
		m := models[model]
		if m == nil {
			m = &modelRow{Model: model, Priced: priced}
			models[model] = m
		}
		addTokens(&m.tokenCounts, r)
		d := days[r.Day.Format("2006-01-02")]
		if d == nil {
			d = &dayRow{Day: r.Day.Format("2006-01-02")}
			days[d.Day] = d
		}
		addTokens(&d.tokenCounts, r)
		ts := sessionsTok[r.SessionID]
		if ts == nil {
			s := byID[r.SessionID]
			ts = &topSession{SessionID: r.SessionID, Title: s.Title, Runtime: orUnknown(s.Runtime), StartedAt: s.StartedAt}
			sessionsTok[r.SessionID] = ts
		}
		addTokens(&ts.tokenCounts, r)
		if priced {
			addCost(&out.Totals, cost)
			addCost(&m.tokenCounts, cost)
			addCost(&d.tokenCounts, cost)
			addCost(&ts.tokenCounts, cost)
		}
	}
	if denom := out.Totals.Input + out.Totals.CacheRead + out.Totals.CacheWrite; denom > 0 {
		out.CacheHitRate = float64(out.Totals.CacheRead) / float64(denom)
	}
	roundCost := func(c *tokenCounts) {
		if c.CostUSD != nil {
			v := roundUSD(*c.CostUSD)
			c.CostUSD = &v
		}
	}
	roundCost(&out.Totals)
	for _, m := range models {
		roundCost(&m.tokenCounts)
		out.ByModel = append(out.ByModel, *m)
	}
	sort.Slice(out.ByModel, func(i, j int) bool {
		a, b := out.ByModel[i], out.ByModel[j]
		if a.Input+a.Output+a.CacheRead+a.CacheWrite != b.Input+b.Output+b.CacheRead+b.CacheWrite {
			return a.Input+a.Output+a.CacheRead+a.CacheWrite > b.Input+b.Output+b.CacheRead+b.CacheWrite
		}
		return a.Model < b.Model
	})
	for _, d := range days {
		roundCost(&d.tokenCounts)
		out.Daily = append(out.Daily, *d)
	}
	sort.Slice(out.Daily, func(i, j int) bool { return out.Daily[i].Day < out.Daily[j].Day })
	var top []topSession
	for id, ts := range sessionsTok {
		if visible(id) {
			roundCost(&ts.tokenCounts)
			top = append(top, *ts)
		}
	}
	sort.Slice(top, func(i, j int) bool {
		ai, aj := top[i].Input+top[i].Output+top[i].CacheWrite, top[j].Input+top[j].Output+top[j].CacheWrite
		if ai != aj {
			return ai > aj
		}
		return top[i].SessionID < top[j].SessionID
	})
	if len(top) > insightsTopN {
		top = top[:insightsTopN]
	}
	out.TopSessions = append(out.TopSessions, top...)
	for m := range unpriced {
		out.UnpricedModels = append(out.UnpricedModels, m)
	}
	sort.Strings(out.UnpricedModels)
	return out
}

func summariseCrons(runs []db.CronRunRow, now time.Time) insightsCrons {
	out := insightsCrons{Status: []countRow{}}
	status := map[string]int{}
	var secs []float64
	for _, r := range runs {
		out.Runs++
		status[r.Status]++
		if r.Late {
			out.Late++
		}
		if r.Manual {
			out.Manual++
		}
		if r.StartedAt != nil {
			end := now
			if r.EndedAt != nil {
				end = *r.EndedAt
			}
			if d := end.Sub(*r.StartedAt).Seconds(); d >= 0 {
				secs = append(secs, d)
			}
		}
	}
	out.Status = sortedCounts(status)
	out.Seconds = quantilesOf(secs)
	return out
}

// ─── prices ───────────────────────────────────────────────────────────────────

// HandleGetInsightPrices is GET /api/insights/prices: the per-model prices the dollar estimates use. Any signed-in
// person may read them; only an administrator changes them.
func (a *API) HandleGetInsightPrices(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authBrowser(w, r); !ok {
		return
	}
	if a.dbPool == nil {
		writeJSON(w, http.StatusOK, []db.ModelPrice{})
		return
	}
	prices, err := db.ListModelPrices(r.Context(), a.dbPool)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read prices")
		return
	}
	writeJSON(w, http.StatusOK, prices)
}

func validModelName(s string) bool {
	if s == "" || len(s) > 200 {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validPrice(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 100000 }

// HandlePutInsightPrice is PUT /api/insights/prices (an administrator): store or replace one model's price.
func (a *API) HandlePutInsightPrice(w http.ResponseWriter, r *http.Request) {
	p, ok := a.requireInsightsAdmin(w, r)
	if !ok {
		return
	}
	var req db.ModelPrice
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Model = strings.TrimSpace(req.Model)
	if !validModelName(req.Model) {
		writeError(w, http.StatusBadRequest, "model must be 1 to 200 characters")
		return
	}
	for _, v := range []float64{req.InputPerMTok, req.OutputPerMTok, req.CacheReadPerMTok, req.CacheWritePerMTok} {
		if !validPrice(v) {
			writeError(w, http.StatusBadRequest, "prices are dollars per million tokens, from 0 to 100000")
			return
		}
	}
	req.UpdatedBy = p.Sub
	if err := db.UpsertModelPrice(r.Context(), a.dbPool, req); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save the price")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleDeleteInsightPrice is DELETE /api/insights/prices?model=... (an administrator).
func (a *API) HandleDeleteInsightPrice(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireInsightsAdmin(w, r); !ok {
		return
	}
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	if !validModelName(model) {
		writeError(w, http.StatusBadRequest, "model required")
		return
	}
	found, err := db.DeleteModelPrice(r.Context(), a.dbPool, model)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete the price")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "no such price")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) requireInsightsAdmin(w http.ResponseWriter, r *http.Request) (identity.Principal, bool) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return identity.Principal{}, false
	}
	if !p.Has(insightsAdminCap) {
		writeError(w, http.StatusForbidden, "only an administrator can change prices")
		return identity.Principal{}, false
	}
	if a.dbPool == nil {
		writeError(w, http.StatusConflict, "prices need the database")
		return identity.Principal{}, false
	}
	return p, true
}
