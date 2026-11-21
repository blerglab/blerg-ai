package livedata

import "time"

// PostureReport travels only between the dedicated Core and Runner identities.
// It contains no machine names, paths, pod manifests or credentials.
type PostureReport struct {
	Safe      bool      `json:"safe"`
	Cause     string    `json:"cause"`
	CheckedAt time.Time `json:"checked_at"`
}

func (p PostureReport) Valid() bool {
	if p.CheckedAt.IsZero() {
		return false
	}
	if p.Safe {
		return p.Cause == "isolated_cluster"
	}
	switch p.Cause {
	case "desktop_container_runtime", "host_runtime", "privileged_runtime":
		return true
	}
	return false
}
