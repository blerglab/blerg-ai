package localinfer_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/blerglab/blerg-ai/board/internal/localinfer"
)

// Every caller decides for itself what a missing or down box means. The
// transport only tells it which of the two happened.
func Example_perCallerPolicy() {
	// A deployment with no local inference wired at all.
	c := localinfer.New(localinfer.Config{})

	_, err := c.Chat(context.Background(), localinfer.ChatRequest{
		Messages: []localinfer.Message{{Role: "user", Content: "summarise this card"}},
	})
	switch {
	case err == nil:
		fmt.Println("summary written")
	case errors.Is(err, localinfer.ErrNotConfigured):
		// A card-digest generator has no fallback and no obligation: skip.
		fmt.Println("no local inference configured — skipping the summary")
	case localinfer.Unavailable(err):
		// The admission gate would fall open here instead (gate_on_unavailable).
		fmt.Println("local inference down — skipping the summary")
	default:
		fmt.Println("summary failed:", err)
	}
	// Output: no local inference configured — skipping the summary
}
