package cardtime

import "fmt"

// NoticeText is the plan-drift fact text for a budget crossing (L9): ids and numbers only — a
// full ticket key with its board context (boardID+"#"+cardNumber, never a bare "#N" ambiguous
// across boards), the threshold reached (in hours, from its own absolute seconds — never an
// interval-relative ordinal), and the review-round count. No worker titles, no session ids, no
// mark prose, no file or repository path.
func NoticeText(boardID string, cardNumber int, thresholdSeconds int64, rounds int) string {
	hours := thresholdSeconds / 3600
	key := fmt.Sprintf("%s#%d", boardID, cardNumber)
	switch rounds {
	case 0:
		return fmt.Sprintf("%s has reached %dh of worker time.", key, hours)
	case 1:
		return fmt.Sprintf("%s has reached %dh of worker time across 1 review round.", key, hours)
	default:
		return fmt.Sprintf("%s has reached %dh of worker time across %d review rounds.", key, hours, rounds)
	}
}
