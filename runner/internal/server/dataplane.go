package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// ─── Embedder ────────────────────────────────────────────────────────────────

// Embedder turns texts into vectors. Server-side only — no embedding key
// ever reaches a daemon or pod. nil embedder → keyword-fallback search.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// voyageEmbedder calls the Voyage AI embeddings API.
type voyageEmbedder struct {
	apiKey string
	model  string
	client *http.Client
}

// NewEmbedderFromEnv returns the configured embedder or nil.
func NewEmbedderFromEnv() Embedder {
	key := os.Getenv("VOYAGE_API_KEY")
	if key == "" {
		return nil
	}
	model := os.Getenv("VOYAGE_MODEL")
	if model == "" {
		model = "voyage-3-lite"
	}
	return &voyageEmbedder{apiKey: key, model: model, client: &http.Client{Timeout: 30 * time.Second}}
}

func (v *voyageEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	payload, _ := json.Marshal(map[string]any{"model": v.model, "input": texts})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.voyageai.com/v1/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+v.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("voyage: %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	vecs := make([][]float32, len(out.Data))
	for i, d := range out.Data {
		vecs[i] = d.Embedding
	}
	return vecs, nil
}

func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return -1
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return -1
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// ─── Handlers ────────────────────────────────────────────────────────────────

type memoryWriteRequest struct {
	Project string `json:"project"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Content string `json:"content"`
}

// HandlePostMemory upserts a memory (bearer: daemon token), embedding at
// write time when an embedder is configured.
func (a *API) HandlePostMemory(w http.ResponseWriter, r *http.Request) {
	if !checkBearerToken(w, r, a.daemonToken) {
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "no database")
		return
	}
	var req memoryWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Project == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "project, name, content required")
		return
	}
	if len(req.Content) > 64<<10 {
		writeError(w, http.StatusBadRequest, "memory content exceeds 64KB — split it")
		return
	}
	if req.Kind == "" {
		req.Kind = "project"
	}
	var embedding []float32
	if a.embedder != nil {
		if vecs, err := a.embedder.Embed(r.Context(), []string{req.Name + "\n" + req.Content}); err == nil && len(vecs) == 1 {
			embedding = vecs[0]
		}
	}
	if err := db.UpsertAgentMemory(r.Context(), a.dbPool, req.Project, req.Name, req.Kind, req.Content, embedding); err != nil {
		writeError(w, http.StatusInternalServerError, "write error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleDeleteMemory removes a memory (bearer: daemon token).
func (a *API) HandleDeleteMemory(w http.ResponseWriter, r *http.Request) {
	if !checkBearerToken(w, r, a.daemonToken) {
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "no database")
		return
	}
	project, name := r.URL.Query().Get("project"), r.URL.Query().Get("name")
	if project == "" || name == "" {
		writeError(w, http.StatusBadRequest, "project and name required")
		return
	}
	if err := db.DeleteAgentMemory(r.Context(), a.dbPool, project, name); err != nil {
		writeError(w, http.StatusInternalServerError, "delete error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type memoryIndexEntry struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
}

// HandleGetMemories returns a project's memory index (bearer: daemon token).
func (a *API) HandleGetMemories(w http.ResponseWriter, r *http.Request) {
	if !checkBearerToken(w, r, a.daemonToken) {
		return
	}
	if a.dbPool == nil {
		writeJSON(w, http.StatusOK, []memoryIndexEntry{})
		return
	}
	rows, err := db.ListAgentMemories(r.Context(), a.dbPool, r.URL.Query().Get("project"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list error")
		return
	}
	out := make([]memoryIndexEntry, 0, len(rows))
	for _, m := range rows {
		summary := m.Content
		if i := strings.IndexByte(summary, '\n'); i > 0 {
			summary = summary[:i]
		}
		if len(summary) > 160 {
			summary = summary[:160]
		}
		out = append(out, memoryIndexEntry{Name: m.Name, Kind: m.Kind, Summary: summary})
	}
	writeJSON(w, http.StatusOK, out)
}

type knowledgeSearchRequest struct {
	Project string `json:"project"`
	Query   string `json:"query"`
	K       int    `json:"k"`
}

type knowledgeHit struct {
	Name    string  `json:"name"`
	Kind    string  `json:"kind"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

// HandleKnowledgeSearch scores the project's memories against the query —
// cosine over stored embeddings when an embedder is configured, keyword
// fallback otherwise (bearer: daemon token).
func (a *API) HandleKnowledgeSearch(w http.ResponseWriter, r *http.Request) {
	if !checkBearerToken(w, r, a.daemonToken) {
		return
	}
	if a.dbPool == nil {
		writeJSON(w, http.StatusOK, []knowledgeHit{})
		return
	}
	var req knowledgeSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Query == "" {
		writeError(w, http.StatusBadRequest, "query required")
		return
	}
	if req.K <= 0 || req.K > 20 {
		req.K = 5
	}
	rows, err := db.ListAgentMemories(r.Context(), a.dbPool, req.Project)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list error")
		return
	}
	var hits []knowledgeHit
	var queryVec []float32
	if a.embedder != nil {
		if vecs, err := a.embedder.Embed(r.Context(), []string{req.Query}); err == nil && len(vecs) == 1 {
			queryVec = vecs[0]
		}
	}
	for _, m := range rows {
		score := -1.0
		if queryVec != nil && len(m.Embedding) > 0 {
			score = cosine(queryVec, m.Embedding)
		} else {
			// Keyword fallback: fraction of query terms present.
			terms := strings.Fields(strings.ToLower(req.Query))
			if len(terms) > 0 {
				text := strings.ToLower(m.Name + " " + m.Content)
				n := 0
				for _, t := range terms {
					if strings.Contains(text, t) {
						n++
					}
				}
				score = float64(n) / float64(len(terms))
			}
		}
		if score > 0 {
			hits = append(hits, knowledgeHit{Name: m.Name, Kind: m.Kind, Content: m.Content, Score: score})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > req.K {
		hits = hits[:req.K]
	}
	if hits == nil {
		hits = []knowledgeHit{}
	}
	writeJSON(w, http.StatusOK, hits)
}

// HandlePostRule inserts a proposed rule, DISABLED until a human approves —
// the executor cannot verify free-text approval, so approval is a UI action
// (bearer: daemon token).
func (a *API) HandlePostRule(w http.ResponseWriter, r *http.Request) {
	if !checkBearerToken(w, r, a.daemonToken) {
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "no database")
		return
	}
	var req struct{ Project, Content string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Project == "" || req.Content == "" {
		writeError(w, http.StatusBadRequest, "project and content required")
		return
	}
	id, err := db.InsertAgentRule(r.Context(), a.dbPool, req.Project, req.Content)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "write error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "status": "pending approval"})
}

// HandleGetRules lists a project's rules. Rules steer what agents do, so the
// read is admin-only: the daemon token or a core token with board.admin.
func (a *API) HandleGetRules(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authDaemonOrCoreCap(w, r, "board.admin"); !ok {
		return
	}
	if a.dbPool == nil {
		writeJSON(w, http.StatusOK, []db.AgentRuleRow{})
		return
	}
	rows, err := db.ListAgentRules(r.Context(), a.dbPool, r.URL.Query().Get("project"), false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list error")
		return
	}
	type ruleOut struct {
		ID      string `json:"id"`
		Project string `json:"project"`
		Content string `json:"content"`
		Enabled bool   `json:"enabled"`
	}
	out := make([]ruleOut, 0, len(rows))
	for _, r := range rows {
		out = append(out, ruleOut{ID: r.ID, Project: r.Project, Content: r.Content, Enabled: r.Enabled})
	}
	writeJSON(w, http.StatusOK, out)
}

// HandlePatchRule enables/disables a rule (the human approval action). Daemon
// token or a core token with board.admin.
func (a *API) HandlePatchRule(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authDaemonOrCoreCap(w, r, "board.admin"); !ok {
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "no database")
		return
	}
	var req struct{ Enabled bool }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := db.SetAgentRuleEnabled(r.Context(), a.dbPool, r.PathValue("id"), req.Enabled); err != nil {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleDeleteRule removes a rule. Daemon token or a core token with
// board.admin.
func (a *API) HandleDeleteRule(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authDaemonOrCoreCap(w, r, "board.admin"); !ok {
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "no database")
		return
	}
	if err := db.DeleteAgentRule(r.Context(), a.dbPool, r.PathValue("id")); err != nil {
		writeError(w, http.StatusInternalServerError, "delete error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
