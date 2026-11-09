package blackboard

import "regexp"

var releaseTagPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)

// ReleaseTag accepts only one distinct version tag. Conflicting tags are a
// decision for Main, never evidence for moving membership automatically.
func ReleaseTag(tags []string) (string, bool) {
	tag := ""
	for _, candidate := range tags {
		if !releaseTagPattern.MatchString(candidate) {
			continue
		}
		if tag != "" && tag != candidate {
			return "", true
		}
		tag = candidate
	}
	return tag, false
}

func releaseTagMembership(item Item, card Card, m Milestone) Item {
	tag, ambiguous := ReleaseTag(card.Tags)
	if !ambiguous && tag != "" && releaseTagPattern.MatchString(m.Name) && tag != m.Name && item.State != "moved_out" {
		item.State = "moved_out"
		item.Reason = "Release tag changed from " + m.Name + " to " + tag
		item.Destination = tag
	}
	return item
}
