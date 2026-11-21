package checklist

import "reflect"

// Compact retains only changed items. A set is itself a complete replacement.
func Compact(before []Item, p Payload) Payload {
	if p.Op == "set" || p.Op == "clear" {
		return p
	}
	p.Delta = true
	p.Total = len(p.Items)
	p.Completed = Done(p.Items)
	old := map[string]Item{}
	next := map[string]bool{}
	for _, it := range before {
		old[it.ID] = it
	}
	for _, it := range p.Items {
		next[it.ID] = true
		if prev, ok := old[it.ID]; !ok || !reflect.DeepEqual(prev, it) {
			p.Updates = append(p.Updates, it)
		}
	}
	for _, it := range before {
		if !next[it.ID] {
			p.Removed = append(p.Removed, it.ID)
		}
	}
	p.Items = nil
	return p
}

// Replay accepts both historical full lists and compact changes.
func Replay(before []Item, p Payload) Payload {
	if !p.Delta {
		return p
	}
	items := append([]Item{}, before...)
	for _, id := range p.Removed {
		for i, it := range items {
			if it.ID == id {
				items = append(items[:i], items[i+1:]...)
				break
			}
		}
	}
	for _, update := range p.Updates {
		found := false
		for i, it := range items {
			if it.ID == update.ID {
				items[i] = update
				found = true
				break
			}
		}
		if !found {
			items = append(items, update)
		}
	}
	if p.Op == "move" && p.Changed != nil {
		for i, it := range items {
			if it.ID == p.Changed.ID {
				j := p.Changed.Index - 1
				if j >= 0 && j < len(items) {
					items = append(items[:i], items[i+1:]...)
					items = append(items, Item{})
					copy(items[j+1:], items[j:])
					items[j] = it
				}
				break
			}
		}
	}
	p.Items = items
	p.Delta = false
	p.Updates = nil
	p.Removed = nil
	return p
}
