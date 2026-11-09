package livetls

import (
	"flag"
	"io"
	"os"
	"path/filepath"
)

// RenewAtStart runs only in the offline init container. The authority Secret is
// never mounted into a runtime container. Without it, optional leaf Secrets may
// be copied, and Available still validates them before opening any listener.
func RenewAtStart(authority, source, output, role string) error {
	if role != "core" && role != "runner" {
		return ErrIdentity
	}
	if os.MkdirAll(output, 0700) != nil {
		return ErrIdentity
	}
	dir, e := os.MkdirTemp("", "live-data-renew-")
	if e != nil {
		return ErrIdentity
	}
	defer func() { _ = os.RemoveAll(dir) }()
	authority = authorityPath(authority)
	target := filepath.Join(dir, "authority")
	if os.Mkdir(target, 0700) != nil {
		return ErrIdentity
	}
	missing := 0
	for _, name := range []string{"identity.json", "key.pem", "ca.pem"} {
		raw, err := os.ReadFile(filepath.Join(authority, name)) // #nosec G304 -- fixed filenames on offline init-only authority mount.
		if os.IsNotExist(err) {
			missing++
			continue
		}
		if err != nil || len(raw) > 16384 {
			return ErrIdentity
		}
		if write(target, name, raw) != nil {
			return ErrIdentity
		}
	}
	from := filepath.Join(dir, role)
	switch missing {
	case 3:
		from = source
	case 0:
		if Provision(dir) != nil {
			return ErrIdentity
		}
	default:
		return ErrIdentity
	}
	for _, name := range []string{"identity.json", "key.pem", "cert.pem", "ca.pem"} {
		raw, err := os.ReadFile(filepath.Join(from, name)) // #nosec G304 -- fixed filenames on trusted init-only bundle directory.
		if os.IsNotExist(err) && missing == 3 {
			continue
		}
		if err != nil || len(raw) > 16384 {
			return ErrIdentity
		}
		if write(output, name, raw) != nil {
			return ErrIdentity
		}
	}
	return nil
}

// RunRenew is an offline startup command shared by both service images.
func RunRenew(args []string) error {
	flags := flag.NewFlagSet("live-data-renew", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	authority := flags.String("authority", "", "offline CA mount")
	source := flags.String("source", "", "optional leaf source")
	output := flags.String("output", "", "runtime bundle")
	role := flags.String("role", "", "service role")
	if flags.Parse(args) != nil || *authority == "" || *source == "" || *output == "" || flags.NArg() != 0 {
		return ErrIdentity
	}
	return RenewAtStart(*authority, *source, *output, *role)
}
