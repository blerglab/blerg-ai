package api

import (
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

// TestBoardRoleModel pins the role→field mapping every spawn path shares:
// each role's own override when set, board.Model otherwise. Roles without a
// field of their own (worker, standing) always land on board.Model.
func TestBoardRoleModel(t *testing.T) {
	full := db.Board{
		Model:         "worker-model",
		ReviewerModel: "reviewer-model",
		DiscussModel:  "discuss-model",
		ChatModel:     "chat-model",
	}
	bare := db.Board{Model: "worker-model"}

	cases := []struct {
		name  string
		board db.Board
		role  string
		want  string
	}{
		{"worker has no override", full, "worker", "worker-model"},
		{"reviewer override", full, "reviewer", "reviewer-model"},
		{"discuss override", full, "discuss", "discuss-model"},
		{"board chat override", full, "board", "chat-model"},
		{"standing has no override", full, "standing", "worker-model"},
		{"unset reviewer falls back", bare, "reviewer", "worker-model"},
		{"unset discuss falls back", bare, "discuss", "worker-model"},
		{"unset chat falls back", bare, "board", "worker-model"},
		{"unknown role falls back", full, "whatever", "worker-model"},
		{"empty board model stays empty", db.Board{}, "discuss", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := boardRoleModel(tc.board, tc.role); got != tc.want {
				t.Errorf("boardRoleModel(%q) = %q, want %q", tc.role, got, tc.want)
			}
		})
	}
}
