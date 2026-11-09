// Package blackboard owns the durable plan of record. It never owns board card state.
package blackboard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"time"
)

var (
	ErrNotFound     = errors.New("blackboard not found")
	ErrConflict     = errors.New("stale plan revision")
	ErrForbidden    = errors.New("only the workspace owner can commit a plan")
	ErrImmutable    = errors.New("history is append-only")
	ErrInvalid      = errors.New("invalid blackboard record")
	ErrFrozen       = errors.New("milestone is frozen")
	ErrNeedsRelease = errors.New("choose a release for the cards still open, or move them out, before Final")
)

type Actor struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}
type Person struct {
	Actor
	LoginID string `json:"-"`
}
type Link struct {
	WorkspaceID string `json:"workspace_id"`
	BoardID     string `json:"board_id"`
}
type Goal struct {
	ID        string `json:"id"`
	Owner     Actor  `json:"owner"`
	Name      string `json:"name"`
	Version   int    `json:"version"`
	Reference string `json:"reference,omitempty"`
}
type Milestone struct {
	ShippedAt          *time.Time      `json:"shipped_at,omitempty"`
	CutReceiptID       string          `json:"cut_receipt_id,omitempty"`
	EvidenceIncomplete bool            `json:"evidence_incomplete,omitempty"`
	LegacyEvidenceID   int64           `json:"legacy_evidence_id,omitempty"`
	LegacyFrozenAt     *time.Time      `json:"legacy_frozen_at,omitempty"`
	ID                 string          `json:"id"`
	Owner              Actor           `json:"owner"`
	Name               string          `json:"name"`
	BoardID            string          `json:"board_id"`
	ReleaseID          string          `json:"release_id"`
	Target             *time.Time      `json:"target"`
	Stage              string          `json:"stage"`
	FrozenAt           *time.Time      `json:"frozen_at"`
	Revision           int             `json:"revision"`
	Version            int             `json:"version"`
	Frozen             json.RawMessage `json:"frozen,omitempty"`
	Reference          string          `json:"reference,omitempty"`
}
type Thrust struct {
	ID          string `json:"id"`
	Owner       Actor  `json:"owner"`
	MilestoneID string `json:"milestone_id"`
	GoalID      string `json:"goal_id"`
	Name        string `json:"name"`
	Version     int    `json:"version"`
	Reference   string `json:"reference,omitempty"`
}
type ItemID string
type Item struct {
	Title       string `json:"title,omitempty"`
	GoalID      string `json:"goal_id,omitempty"`
	Progress    string `json:"progress,omitempty"`
	ID          ItemID `json:"id"`
	MilestoneID string `json:"milestone_id"`
	BoardID     string `json:"board_id"`
	CardNumber  int    `json:"card_number"`
	ThrustID    string `json:"thrust_id"`
	Reason      string `json:"reason"`
	RaisedBy    Actor  `json:"raised_by"`
	State       string `json:"state"`
	Destination string `json:"destination,omitempty"`
}
type Event struct {
	MetadataOmitted    bool            `json:"metadata_omitted,omitempty"`
	ID                 string          `json:"id"`
	MilestoneID        string          `json:"milestone_id"`
	AuthenticatedActor Actor           `json:"authenticated_actor"`
	RecordedAt         time.Time       `json:"recorded_at"`
	ClaimedAt          *time.Time      `json:"claimed_at"`
	Source             string          `json:"source"`
	Kind               string          `json:"kind"`
	Payload            json.RawMessage `json:"payload"`
}
type Revision struct {
	Thrusts         []Thrust `json:"thrusts,omitempty"`
	MetadataOmitted bool     `json:"metadata_omitted,omitempty"`
	MilestoneID     string   `json:"milestone_id"`
	Number          int      `json:"number"`
	Items           []Item   `json:"items"`
	EventID         string   `json:"event_id"`
}

// ThrustChange stores server-resolved names with the immutable proposal.
type ThrustChange struct {
	ID              string `json:"id,omitempty"`
	Name            string `json:"name,omitempty"`
	GoalID          string `json:"goal_id,omitempty"`
	Reason          string `json:"reason"`
	Destination     string `json:"destination,omitempty"`
	PreviousName    string `json:"previous_name,omitempty"`
	DestinationName string `json:"destination_name,omitempty"`
	GoalName        string `json:"goal_name,omitempty"`
}
type Change struct {
	Thrust    *ThrustChange `json:"thrust,omitempty"`
	ID        ItemID        `json:"id"`
	Operation string        `json:"operation"`
	Item      Item          `json:"item"`
}
type ProposalID string
type Proposal struct {
	MetadataOmitted bool       `json:"metadata_omitted,omitempty"`
	ID              ProposalID `json:"id"`
	MilestoneID     string     `json:"milestone_id"`
	Revision        int        `json:"revision"`
	Actor           Actor      `json:"actor"`
	CreatedAt       time.Time  `json:"created_at"`
	Changes         []Change   `json:"changes"`
	Status          string     `json:"status"`
	Selected        []ItemID   `json:"selected,omitempty"`
}
type Snapshot struct {
	MetadataOmitted bool           `json:"metadata_omitted,omitempty"`
	MilestoneID     string         `json:"milestone_id"`
	Day             string         `json:"day"`
	ObservedAt      time.Time      `json:"observed_at"`
	Complete        bool           `json:"complete"`
	Counts          map[string]int `json:"counts"`
}
type Record struct {
	Usage        *PartitionUsage `json:"-"`
	WorkspaceID  string          `json:"workspace_id,omitempty"`
	PlanRevision int             `json:"plan_revision"`
	ID           string          `json:"id"`
	Owner        Actor           `json:"owner"`
	ParentID     *string         `json:"parent_id"`
	Name         string          `json:"name"`
	Vision       string          `json:"vision"`
	Version      int             `json:"version"`
	Style        json.RawMessage `json:"style"`
	Mapping      Mapping         `json:"mapping"`
	Links        []Link          `json:"links"`
	Goals        []Goal          `json:"goals"`
	Milestones   []Milestone     `json:"milestones"`
	Thrusts      []Thrust        `json:"thrusts"`
	Items        []Item          `json:"items"`
	Revisions    []Revision      `json:"revisions"`
	Events       []Event         `json:"events"`
	Proposals    []Proposal      `json:"proposals"`
	Snapshots    []Snapshot      `json:"snapshots"`
}

// Mapping is stored per blackboard; empty fields select the default mapping.
type Mapping struct {
	DoneColumns []string `json:"done_columns"`
	WorkColumns []string `json:"work_columns"`
	TestTag     string   `json:"test_tag"`
	NeedsTag    string   `json:"needs_tag"`
}

// Store callbacks execute under a per-record lock. A failed callback has no effects.
// Implementations must reject mutation or removal of existing events and revisions.
type Store interface {
	Create(context.Context, Record) error
	Load(context.Context, string) (Record, error)
	Update(context.Context, string, func(*Record) error) error
}

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func Clone(r Record) Record {
	b, _ := json.Marshal(r)
	var c Record
	_ = json.Unmarshal(b, &c)
	if r.Usage != nil {
		u := *r.Usage
		c.Usage = &u
	}
	return c
}
func ValidateUpdate(old, next Record) error {
	if err := validateBounds(next); err != nil {
		return err
	}
	if old.ID != next.ID || old.Owner != next.Owner || !reflect.DeepEqual(old.ParentID, next.ParentID) {
		return ErrImmutable
	}
	if len(next.Events) < len(old.Events) || len(next.Revisions) < len(old.Revisions) {
		return ErrImmutable
	}
	for i := range old.Events {
		if !sameJSON(old.Events[i], next.Events[i]) {
			return ErrImmutable
		}
	}
	if len(next.Snapshots) < len(old.Snapshots) {
		return ErrImmutable
	}
	for i := range old.Snapshots {
		a, b := old.Snapshots[i], next.Snapshots[i]
		if !sameJSON(a, b) && (a.Complete || !b.Complete || a.MilestoneID != b.MilestoneID || a.Day != b.Day || b.ObservedAt.Before(a.ObservedAt)) {
			return ErrImmutable
		}
	}
	for i := range old.Revisions {
		if !sameJSON(old.Revisions[i], next.Revisions[i]) {
			return ErrImmutable
		}
	}
	return nil
}

// ValidateCreate checks identity before persistence.
func ValidateCreate(r Record) error {
	if err := validateBounds(r); err != nil {
		return err
	}
	if r.ID == "" || r.Owner.ID == "" || r.Owner.Kind == "" || r.Version < 1 {
		return ErrInvalid
	}
	return nil
}

// Automation limits are per admission partition, separate from plan capacity.
const MaxEvents = 10000
const MaxAutomaticEvents = MaxEvents - 1000
const MaxRecordBytes = 16 << 20

func automaticCapacity(r Record) bool {
	u := r.Usage
	if u == nil {
		u = &PartitionUsage{Events: len(r.Events)}
		for _, e := range r.Events {
			data, _ := json.Marshal(e)
			u.EventBytes += len(data)
		}
	}
	return u.Events < MaxAutomaticEvents && u.EventBytes < MaxPartitionEventBytes-(32<<10) && u.WorkspaceEvents < MaxWorkspaceEvents && u.WorkspaceEventBytes < MaxWorkspaceEventBytes-(32<<10)
}

func validateBounds(r Record) error {
	// Audit/proposal rows have their own admission budgets. They cannot consume
	// the person's authoritative plan capacity, even in legacy overfull records.
	data, err := json.Marshal(Header(r))
	if err != nil || len(data) > MaxRecordBytes {
		return ErrInvalid
	}
	return nil
}

// MemoryStore is the provider-owned fake, with the same atomicity and copy isolation as Postgres.
type MemoryStore struct {
	mu      sync.Mutex
	rows    map[string]Record
	history map[string]Record
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rows: map[string]Record{}, history: map[string]Record{}}
}
func (s *MemoryStore) Create(_ context.Context, r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ValidateCreate(r); err != nil {
		return err
	}
	if _, ok := s.rows[r.ID]; ok {
		return ErrConflict
	}
	s.put(Clone(r))
	return nil
}
func (s *MemoryStore) Load(_ context.Context, id string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok {
		return Record{}, ErrNotFound
	}
	return Clone(s.join(r)), nil
}
func (s *MemoryStore) Update(_ context.Context, id string, fn func(*Record) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.rows[id]
	if !ok {
		return ErrNotFound
	}
	old = s.join(old)
	next := Clone(old)
	if err := fn(&next); err != nil {
		return err
	}
	if err := ValidateUpdate(old, next); err != nil {
		return err
	}
	s.put(Clone(next))
	return nil
}

// Postgres jsonb reformats payloads. Compare their values, never whitespace.
func sameJSON(a, b any) bool {
	x, e := json.Marshal(a)
	if e != nil {
		return false
	}
	y, e := json.Marshal(b)
	if e != nil {
		return false
	}
	var left, right any
	if json.Unmarshal(x, &left) != nil || json.Unmarshal(y, &right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

// ReferencedBoards includes historical membership: unlinking a board never
// removes the live permission fence on its frozen titles and decisions.
func ReferencedBoards(r Record) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, l := range r.Links {
		add(l.BoardID)
	}
	for _, m := range r.Milestones {
		add(m.BoardID)
	}
	for _, item := range r.Items {
		add(item.BoardID)
	}
	for _, rev := range r.Revisions {
		for _, item := range rev.Items {
			add(item.BoardID)
		}
	}
	return out
}
