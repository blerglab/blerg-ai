package daemon

// mergeSessionIDs returns the de-duplicated union of any number of ID slices.
func mergeSessionIDs(groups ...[]string) []string {
	total := 0
	for _, g := range groups {
		total += len(g)
	}
	seen := make(map[string]struct{}, total)
	out := make([]string, 0, total)
	for _, group := range groups {
		for _, id := range group {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}
