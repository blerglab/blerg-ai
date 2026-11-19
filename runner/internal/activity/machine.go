package activity

import "time"

type Machine struct {
	SessionID     string    `json:"session_id"`
	At            time.Time `json:"at"`
	Load1         float64   `json:"load_1"`
	CPUs          int       `json:"cpus"`
	DiskFreeBytes uint64    `json:"disk_free_bytes"`
}
