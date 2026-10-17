package protocol

import (
	"encoding/json"
	"unicode"
	"unicode/utf8"
)

const (
	MaxBackgroundTaskSessions = 1024
	MaxBackgroundTasks        = 4096
	MaxBackgroundTaskIDBytes  = 256
	// Leaves room for the other fields in the server's 4MiB hello frame.
	MaxBackgroundInventoryBytes = 2 << 20
)

// BackgroundTaskInventory is a complete current-lifetime snapshot, containing
// opaque IDs only. A nil inventory means unknown/old peer; an empty non-nil
// Sessions map means no tasks. Missing session entries in a valid full snapshot
// mean no currently known tasks for that session. No truncated lists are valid.
type BackgroundTaskInventory struct {
	Lifetime string              `json:"lifetime"`
	Sessions map[string][]string `json:"sessions"`
}

func ValidBackgroundTaskInventory(inventory *BackgroundTaskInventory) bool {
	if inventory == nil || !backgroundUUID(inventory.Lifetime) || inventory.Sessions == nil || len(inventory.Sessions) > MaxBackgroundTaskSessions {
		return false
	}
	total := 0
	for session, tasks := range inventory.Sessions {
		if !backgroundUUID(session) || tasks == nil {
			return false
		}
		total += len(tasks)
		if total > MaxBackgroundTasks {
			return false
		}
		seen := make(map[string]bool, len(tasks))
		for _, task := range tasks {
			if !ValidBackgroundTaskID(task) || seen[task] {
				return false
			}
			seen[task] = true
		}
	}
	raw, err := json.Marshal(inventory)
	return err == nil && len(raw) <= MaxBackgroundInventoryBytes
}

// ValidBackgroundTaskID permits engines' printable opaque IDs, without
// assuming UUIDs or a provider-specific alphabet. Metadata is never clipped.
func ValidBackgroundTaskID(id string) bool {
	if id == "" || len(id) > MaxBackgroundTaskIDBytes || !utf8.ValidString(id) {
		return false
	}
	for _, r := range id {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}
func backgroundUUID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i := range len(id) {
		switch i {
		case 8, 13, 18, 23:
			if id[i] != '-' {
				return false
			}
		default:
			if (id[i] < '0' || id[i] > '9') && (id[i] < 'a' || id[i] > 'f') {
				return false
			}
		}
	}
	return id[14] >= '1' && id[14] <= '8' && (id[19] == '8' || id[19] == '9' || id[19] == 'a' || id[19] == 'b')
}
