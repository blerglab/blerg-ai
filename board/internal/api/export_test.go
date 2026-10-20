package api

// HubSubscriberCountForTest observes registration without guessing at scheduler timing.
func HubSubscriberCountForTest(h *Hub, boardID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs[boardID])
}
