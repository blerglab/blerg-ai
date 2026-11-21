package shellruntime

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
)

func TestPersistentProbeRequiresClockAndExactMember(t *testing.T) {
	root := t.TempDir()
	identity, err := ResolvePersistentIdentity(root)
	if err != nil {
		t.Skip("strict account resolver unavailable")
	}
	req := persistentProbeRequest{Version: 2, Challenge: "fresh-challenge", Cwd: root, PublicRoot: root, Root: identity.MemberRoot}
	encode := func(r persistentProbeRequest) *bytes.Reader {
		data, e := json.Marshal(r)
		if e != nil {
			t.Fatal(e)
		}
		return bytes.NewReader(data)
	}
	clock := func() (ownershell.LeaseClock, error) {
		return func() (time.Time, time.Duration, error) { return time.Now(), time.Second, nil }, nil
	}
	for _, kind := range []string{"valid", "clock-unavailable", "root-replaced", "wrong-version", "no-challenge", "outside-member"} {
		t.Run(kind, func(t *testing.T) {
			request := req
			factory := clock
			switch kind {
			case "clock-unavailable":
				factory = func() (ownershell.LeaseClock, error) { return nil, ErrClosed }
			case "root-replaced":
				request.Root.Inode++
			case "wrong-version":
				request.Version = 1
			case "outside-member":
				request.Cwd = t.TempDir()
			case "no-challenge":
				request.Challenge = ""
			}
			var out bytes.Buffer
			err := probePersistent(encode(request), &out, ResolvePersistentIdentity, factory)
			if kind != "valid" {
				if err == nil || out.Len() != 0 {
					t.Fatal("unproved persistence advertised")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var reply persistentProbeResponse
			if json.Unmarshal(out.Bytes(), &reply) != nil || reply.Version != 2 || reply.Challenge != req.Challenge || reply.Root != req.Root || reply.Identity != identity {
				t.Fatal("probe lost physical identity")
			}
		})
	}
}
