package server

// board_events.go — server→browser event structs for live board updates.
// These types are defined in the server package (not protocol) because they
// embed boardInfo/columnInfo/ticketInfo, which are server-local view types.
// The subscribe/unsubscribe request types live in internal/protocol/messages.go.

// boardCreatedEvent is broadcast to subscribers of boardID when the board is created.
// (In practice, no browsers are subscribed to a brand-new board yet, but the
// event is wired for completeness and future "all-boards" subscriptions.)
type boardCreatedEvent struct {
	Type  string    `json:"type"` // "board_created"
	Board boardInfo `json:"board"`
	OpID  string    `json:"op_id"`
}

// columnChangedEvent is broadcast when a column is created or mutated (rename /
// reorder / set-terminal). The full column state is included so clients can
// upsert in place.
type columnChangedEvent struct {
	Type   string     `json:"type"` // "column_changed"
	Column columnInfo `json:"column"`
	OpID   string     `json:"op_id"`
}

// columnRemovedEvent is broadcast when a column is deleted.
type columnRemovedEvent struct {
	Type     string `json:"type"` // "column_removed"
	ColumnID string `json:"column_id"`
	BoardID  string `json:"board_id"`
	OpID     string `json:"op_id"`
}

// ticketCreatedEvent is broadcast when a ticket is created.
type ticketCreatedEvent struct {
	Type   string     `json:"type"` // "ticket_created"
	Ticket ticketInfo `json:"ticket"`
	OpID   string     `json:"op_id"`
}

// ticketUpdatedEvent is broadcast when a ticket's scalar fields change.
type ticketUpdatedEvent struct {
	Type   string     `json:"type"` // "ticket_updated"
	Ticket ticketInfo `json:"ticket"`
	OpID   string     `json:"op_id"`
}

// ticketMovedEvent is broadcast when a ticket changes column (or rank within a
// column). FromColumnID may be nil when the ticket had no prior column.
type ticketMovedEvent struct {
	Type         string     `json:"type"` // "ticket_moved"
	Ticket       ticketInfo `json:"ticket"`
	FromColumnID *string    `json:"from_column_id"`
	ToColumnID   *string    `json:"to_column_id"`
	OpID         string     `json:"op_id"`
}

// ticketSplitEvent is broadcast when a ticket is split into children.
// OriginID is the source ticket (now archived); OriginTicket carries its
// post-split state (archived_at set, column_id null) so clients holding the
// origin in a cache can update it without a reload; Children are the new tickets.
type ticketSplitEvent struct {
	Type         string       `json:"type"` // "ticket_split"
	OriginID     string       `json:"origin_id"`
	OriginTicket ticketInfo   `json:"origin_ticket"`
	Children     []ticketInfo `json:"children"`
	OpID         string       `json:"op_id"`
}

// ticketArchivedEvent is broadcast when a ticket is archived.
type ticketArchivedEvent struct {
	Type   string     `json:"type"` // "ticket_archived"
	Ticket ticketInfo `json:"ticket"`
	OpID   string     `json:"op_id"`
}

// ticketDependencyChangedEvent is broadcast when a ticket dependency edge is
// added or removed. Action is "added" or "removed".
type ticketDependencyChangedEvent struct {
	Type              string `json:"type"` // "ticket_dependency_changed"
	TicketID          string `json:"ticket_id"`
	DependsOnTicketID string `json:"depends_on_ticket_id"`
	Action            string `json:"action"` // "added" | "removed"
	OpID              string `json:"op_id"`
}
