package ownershell

import "testing"

func TestShellNameSurvivesReattachAndIsOwnerOnly(t *testing.T) {
	f := setup(t)
	info, at := f.open(t)
	named, err := f.m.Rename(t.Context(), f.actor, info.Handle, "  Test watcher  ")
	if err != nil || named.Name != "Test watcher" {
		t.Fatalf("rename: %+v %v", named, err)
	}
	f.m.Detach(f.actor, info.Handle, at.ID)
	if _, err = f.m.Attach(t.Context(), f.actor, info.Handle, false); err != nil {
		t.Fatal(err)
	}
	listed, err := f.m.List(t.Context(), f.actor, f.r.binding.Session)
	if err != nil || len(listed) != 1 || listed[0].Name != "Test watcher" {
		t.Fatalf("list: %+v %v", listed, err)
	}
	other := f.actor
	other.Login = "other"
	if _, err = f.m.Rename(t.Context(), other, info.Handle, "stolen"); err == nil {
		t.Fatal("other login renamed shell")
	}
	if _, err = f.m.Rename(t.Context(), f.actor, info.Handle, "bad\nname"); err == nil {
		t.Fatal("control accepted")
	}
	if _, err = f.m.Rename(t.Context(), f.actor, info.Handle, ""); err != nil {
		t.Fatal(err)
	}
}

func TestShellReportEndReasonsAreExactAndOwnerOnly(t *testing.T) {
	for _, reason := range []Reason{ReasonRequested, ReasonRevoked, ReasonUnavailable, ReasonRuntime, ReasonShutdown, ReasonIdle, ReasonDetached} {
		t.Run(string(reason), func(t *testing.T) {
			f := setup(t)
			info, _ := f.open(t)
			f.m.mu.Lock()
			f.m.closeLocked(t.Context(), f.m.shells[info.Handle], reason)
			f.m.mu.Unlock()
			got, _, ok := f.m.EndReason(f.actor, info.Handle)
			if !ok || got != reason {
				t.Fatalf("got %q, %v", got, ok)
			}
			other := f.actor
			other.Login = "other"
			if _, _, ok := f.m.EndReason(other, info.Handle); ok {
				t.Fatal("end reason leaked")
			}
		})
	}
}

func TestShellTakeoverIsAnAttachmentNotice(t *testing.T) {
	f := setup(t)
	info, first := f.open(t)
	if _, err := f.m.Attach(t.Context(), f.actor, info.Handle, true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.TakenOver:
	default:
		t.Fatal("old controller did not receive takeover")
	}
	found := false
	for _, event := range f.audit.events {
		if event.Event == Event("takeover") && event.Actor == f.actor.Owner && event.Login == f.actor.Login && event.Shell == info.Shell && event.Outcome == "success" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing successful takeover audit", f.audit.events)
	}
	if !f.alive(info.Handle) {
		t.Fatal("takeover ended shell")
	}
}

func TestShellNameRejectsInvisibleAndSeparatorCharacters(t *testing.T) {
	for _, r := range []rune{0x202e, 0x2066, 0x2069, 0x200b, 0xfeff, 0xad, 0x2028, 0x2029} {
		for _, name := range []string{string(r) + "name", "name" + string(r), "na" + string(r) + "me"} {
			if _, err := NormalizeName(name); err == nil {
				t.Errorf("accepted %q", name)
			}
		}
	}
}
