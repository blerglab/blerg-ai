package server

import (
	"context"
	_ "embed"
	"net/http"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Exact Unicode emoji sequences also exclude controls, bidi overrides, isolated
// modifiers, arbitrary ZWJ chains and multiple graphemes.
//
//go:embed session_emoji.txt
var sessionEmojiData string

var sessionEmojis = func() map[string]bool {
	result := map[string]bool{"": true}
	for _, line := range strings.Split(sessionEmojiData, "\n") {
		if !strings.HasPrefix(line, "# ") {
			result[line] = true
		}
	}
	return result
}()

func (a *API) setSessionEmoji(ctx context.Context, sid, emoji string) *APIError {
	if !sessionEmojis[emoji] {
		return apiErrorf(http.StatusUnprocessableEntity, "Choose a single emoji, or leave it empty.")
	}
	if a.dbPool == nil {
		return apiErrorf(http.StatusServiceUnavailable, "database not available")
	}
	tag, err := a.dbPool.Exec(ctx, "UPDATE sessions SET emoji=$2 WHERE id=$1", sid, emoji)
	if err != nil {
		return apiErrorf(http.StatusInternalServerError, "update failed")
	}
	if tag.RowsAffected() == 0 {
		return apiErrorf(http.StatusNotFound, "session not found")
	}
	a.hub.BroadcastJSON(protocol.SessionEmojiChanged{Type: "session_emoji_changed", SessionID: sid, Emoji: emoji}) //nolint:contextcheck // broadcasts have independent bounded delivery checks and outlive this request
	return nil
}
