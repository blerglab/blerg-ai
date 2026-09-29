package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/blerglab/blerg-ai/board/internal/localinfer"
)

// buildPrompt renders the curator prompt. The candidate set is bounded — the
// curator never sees the whole board.
func buildPrompt(in Input) string {
	var b strings.Builder
	b.WriteString(`You are the admission curator for a card board used by AI agents.
Judge ONE operation. Respond with ONLY a JSON object, no prose, no fences:
{"decision":"accept"|"deny"|"revise","reason":"<short, actionable — the agent reads this>","duplicate_of":<card number or null>,"suggestion":"<how to revise, when decision=revise>","confidence":<0..1>}

`)
	if in.Operation == "create" {
		b.WriteString(`Rules for CREATE:
- "deny" for semantic duplicates of an existing card (set duplicate_of to that card's number) or for spam/noise.
- "revise" ONLY when the title could mean materially different pieces of work, or the card batches unrelated changes that should be split.
- Specificity is judged by card type. A BUG title is specific when it names the misbehavior and where it occurs. A FEATURE or TASK title is specific when it says what to add or change and where — features have no "broken behavior" to name; NEVER ask a feature card for one.
- "accept" everything else. Do not deny or revise for style, tone, or wording you would merely phrase differently. When unsure, accept.
- A different wording of the same finding IS a duplicate. A related-but-distinct task is NOT.

`)
	} else {
		b.WriteString(`Rules for ` + strings.ToUpper(in.Operation) + ` (a mutation of the existing card shown below):
- This is normal board work: agents claim cards by moving them, annotate bodies, attach links, archive finished work. ACCEPT by default.
- Partial or small payloads are NORMAL (patch semantics) — never deny as "empty".
- Duplicate detection does NOT apply to mutations. Never deny because the card "already exists" — of course it exists; it is being modified.
- "deny" ONLY for clear vandalism: wholesale deletion of meaningful content, a nonsense retitle, or moving/archiving an incident card (type "incident").
- "revise" is almost never appropriate for mutations.

`)
	}
	fmt.Fprintf(&b, "Board: %s\nOperation: %s\n", in.Board.Name, in.Operation)
	if len(in.Board.FieldSchema) > 2 {
		fmt.Fprintf(&b, "Board field schema: %s\n", string(in.Board.FieldSchema))
	}
	if len(in.Columns) > 0 {
		names := make([]string, len(in.Columns))
		for i, c := range in.Columns {
			names[i] = c.Name
		}
		fmt.Fprintf(&b, "Columns: %s\n", strings.Join(names, ", "))
	}
	if in.Current != nil {
		cur, _ := json.Marshal(map[string]any{
			"number": in.Current.Number, "type": in.Current.Type,
			"title": in.Current.Title, "body": in.Current.Body,
			"column_id": in.Current.ColumnID, "tags": in.Current.Tags,
		})
		fmt.Fprintf(&b, "\nThe card being modified:\n%s\n", cur)
	}
	if len(in.Candidates) > 0 {
		b.WriteString("\nExisting cards most similar to this submission:\n")
		for _, c := range in.Candidates {
			body := ""
			if c.Body != nil {
				body = *c.Body
				if len(body) > 200 {
					body = body[:200] + "…"
				}
			}
			fmt.Fprintf(&b, "- #%d [%s] %s — %s\n", c.Number, c.Type, c.Title, body)
		}
	}
	fmt.Fprintf(&b, "\nSubmission (JSON):\n%s\n", string(in.Payload))
	if in.PriorReviseReason != "" {
		fmt.Fprintf(&b, "\nPrevious round: you returned \"revise\" for this agent's submission (reason: %s). The agent has revised accordingly. Do not re-litigate specificity — accept unless this is now a true duplicate.\n", in.PriorReviseReason)
	}
	if in.PriorDenialReason != "" {
		fmt.Fprintf(&b, "\nThis is a DISPUTE. You are an independent adjudicator: judge both the submission and the prior denial on their merits.\nPrior denial reason: %s\n", in.PriorDenialReason)
		if in.Rebuttal != "" {
			fmt.Fprintf(&b, "Agent's rebuttal: %s\n", in.Rebuttal)
		}
	}
	return b.String()
}

// parseVerdict tolerates fenced or prefixed output around the JSON object.
func parseVerdict(text string) (Verdict, error) {
	start := strings.IndexByte(text, '{')
	end := strings.LastIndexByte(text, '}')
	if start < 0 || end <= start {
		return Verdict{}, fmt.Errorf("no JSON object in curator output")
	}
	var v Verdict
	if err := json.Unmarshal([]byte(text[start:end+1]), &v); err != nil {
		return Verdict{}, fmt.Errorf("curator output not valid verdict JSON: %w", err)
	}
	return v, nil
}

// ── Claude backend (Anthropic API) ───────────────────────────────────────────

type ClaudeBackend struct {
	client anthropic.Client
	model  string
}

// NewClaudeBackend uses the official SDK; apiKey empty ⇒ ANTHROPIC_API_KEY /
// ambient credentials. Defaults to claude-opus-5 with a server-side refusal
// fallback to claude-opus-4-8 (the gate must not die on a classifier decline).
func NewClaudeBackend(apiKey, model string) *ClaudeBackend {
	opts := []option.RequestOption{}
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	if model == "" {
		model = "claude-opus-5"
	}
	return &ClaudeBackend{client: anthropic.NewClient(opts...), model: model}
}

func (c *ClaudeBackend) Name() string    { return "claude" }
func (c *ClaudeBackend) ModelID() string { return c.model }

func (c *ClaudeBackend) Review(ctx context.Context, in Input) (Verdict, error) {
	resp, err := c.client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model:     c.model,
		MaxTokens: 1024,
		// Category-routed server-side fallback: a classifier decline is
		// re-served by Anthropic's recommended model inside the same call.
		Betas:     []anthropic.AnthropicBeta{"server-side-fallback-2026-07-01"},
		Fallbacks: anthropic.BetaFallbacksParamOfDefault(),
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(buildPrompt(in))),
		},
	})
	if err != nil {
		return Verdict{}, err
	}
	if resp.StopReason == "refusal" {
		// Whole fallback chain declined — treat as unavailable, not a deny.
		return Verdict{}, fmt.Errorf("claude declined to judge (refusal)")
	}
	var text strings.Builder
	for _, block := range resp.Content {
		if tb, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(tb.Text)
		}
	}
	return parseVerdict(text.String())
}

// ── OpenAI-compatible backend (local inference, or any /v1/chat/completions
// endpoint) ──────────────────────────────────────────────────────────────────

// OpenAIBackend is the curator running on an OpenAI-compatible endpoint —
// in this deployment the local box, first in the backend order because its
// per-write cost is ~0. It owns the curator prompt and the verdict parse;
// the HTTP lives in internal/localinfer, shared with every other feature
// that offloads to the same endpoint.
type OpenAIBackend struct {
	client *localinfer.Client
	model  string
}

// NewOpenAIBackend adapts a shared local-inference client to the gate's
// Backend interface. model overrides the client's configured chat model;
// pass "" to use it.
func NewOpenAIBackend(client *localinfer.Client, model string) *OpenAIBackend {
	if model == "" {
		model = client.ChatModel()
	}
	return &OpenAIBackend{client: client, model: model}
}

func (r *OpenAIBackend) Name() string    { return "openai" }
func (r *OpenAIBackend) ModelID() string { return r.model }

func (r *OpenAIBackend) Review(ctx context.Context, in Input) (Verdict, error) {
	// A curator pass is a classifier call: temperature 0, and a short cap —
	// the answer is one JSON object.
	resp, err := r.client.Chat(ctx, localinfer.ChatRequest{
		Model:       r.model,
		Messages:    []localinfer.Message{{Role: "user", Content: buildPrompt(in)}},
		Temperature: localinfer.Temperature(0),
		MaxTokens:   512,
	})
	if err != nil {
		// Typed: the gate's caller sees a failed backend and falls through
		// to the next one, then to gate_on_unavailable.
		return Verdict{}, err
	}
	return parseVerdict(resp.Text)
}
