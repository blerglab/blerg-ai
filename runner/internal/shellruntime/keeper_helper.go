package shellruntime

import (
	"io"
	"time"
)

// ServeKeeperHelper is a fixed private entry point selected by the host keeper.
// WSL can prove a short restart interval, never advertise host sleep safety.
func ServeKeeperHelper(in io.Reader, out io.Writer) error {
	return serveHelperWithResolvers(in, out, ResolveIdentity, ResolvePersistentIdentity, time.Now, keeperRestartClock)
}
func ProbeKeeperPersistence(in io.Reader, out io.Writer) error {
	return probePersistent(in, out, ResolvePersistentIdentity, keeperRestartClock)
}
