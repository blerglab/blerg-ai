//go:build linux

package processtelemetry

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type SampleRoot struct {
	PID      int
	Identity string
}
type Sampler struct {
	previous   map[int]Span
	last       time.Time
	identities map[int]string
}

func NewSampler() *Sampler { return &Sampler{previous: map[int]Span{}} }

// ProcessIdentity uses the kernel start clock to reject recycled root PIDs.
func ProcessIdentity(pid int) string {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return ""
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 {
		return ""
	}
	return fields[19]
}

// Sample walks only descendants of explicitly admitted runtime roots. It never
// enumerates other sessions or host processes; work and retained state are capped.
func (s *Sampler) Sample(roots []SampleRoot, now time.Time) Batch {
	current := map[int]Span{}
	identities := map[int]string{}
	threadBudget := MaxProcesses
	seen := map[int]bool{}
	var queue []int
	rootOf := map[int]int{}
	for _, root := range roots {
		if root.PID > 1 && root.Identity != "" && ProcessIdentity(root.PID) == root.Identity {
			queue = append(queue, root.PID)
			rootOf[root.PID] = root.PID
		}
	}
	dropped := uint64(0)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		if len(seen) >= MaxProcesses {
			dropped++
			break
		}
		seen[pid] = true
		if p, c := ReadImage(pid); p != "" {
			identities[pid] = ProcessIdentity(pid)
			current[pid] = Span{PID: pid, Branch: pid, Root: rootOf[pid], Program: p, Class: c, Start: now, End: now, Approximate: true, Basis: "proc_sample"}
		}
		threads, err := os.ReadDir("/proc/" + strconv.Itoa(pid) + "/task")
		if err != nil {
			continue
		}
		for i, thread := range threads {
			if i >= 256 || threadBudget <= 0 {
				dropped++
				break
			}
			threadBudget--
			raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/task/" + thread.Name() + "/children")
			if err != nil {
				continue
			}
			for _, child := range strings.Fields(string(raw)) {
				n, err := strconv.Atoi(child)
				if err == nil && !seen[n] && len(queue)+len(seen) < MaxProcesses {
					queue = append(queue, n)
					rootOf[n] = rootOf[pid]
				}
			}
		}
	}
	result := Batch{Dropped: dropped}
	// Conservative lower bound: count only images seen at both endpoints.
	for pid, p := range current {
		if previous, ok := s.previous[pid]; ok && previous.Program == p.Program && previous.Root == p.Root && identities[pid] != "" && s.identities[pid] == identities[pid] && now.After(s.last) {
			p.Start = s.last
			result.Spans = append(result.Spans, p)
			if len(result.Spans) >= MaxSpans {
				result.Dropped++
				break
			}
		}
	}
	s.previous = current
	s.identities = identities
	s.last = now
	return result
}
