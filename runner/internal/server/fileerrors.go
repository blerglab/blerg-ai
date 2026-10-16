package server

// Failures of the runner's own making on the file and topic routes (its storage, its database),
// answered so that the person who sees them can act and whoever runs Blerg can find them:
//
//	{"error": "<one plain sentence: what failed, the kind of failure, what to do>", "error_id": "<8 hex>"}
//
// and one log line carrying the same error_id and the underlying error. The sentence never
// carries a path, a token or the raw error; the log line carries the raw error, whose paths are
// the runner's own data directory.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"syscall"
)

// noDatabaseMsg is the 503 of a file or topic route on a runner started without a database.
const noDatabaseMsg = "The runner has no database, which files and topics need; whoever runs Blerg needs to set BLERG_RUNNER_DATABASE_URL."

// newErrorID is a short random id that ties a reply to its log line.
func newErrorID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(b[:])
}

// writeFailure logs logContext and err under a fresh error id and answers status with msg and
// the id. It returns the id (for a test, or a caller that logs more).
func writeFailure(w http.ResponseWriter, status int, msg, logContext string, err error) string {
	id := newErrorID()
	log.Printf("%s: %v (error_id=%s)", logContext, err, id)
	writeJSON(w, status, map[string]string{"error": msg, "error_id": id})
	return id
}

// The access a storage step needed, for the permission sentence.
const (
	accessWrite = "write to"
	accessRead  = "read"
)

// storageKind names what went wrong with the runner's disk, in the words the sentence uses, and
// what to do about it. access is what the step needed of the data folder (accessWrite, accessRead).
func storageKind(err error, access string) (kind, todo string) {
	switch {
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return "its disk is full", "free some space on the runner's data volume, or make the volume larger"
	case errors.Is(err, syscall.EROFS):
		return "its data folder is read-only", "whoever runs Blerg needs to make the runner's data volume writable"
	case errors.Is(err, fs.ErrPermission):
		return "it is not allowed to " + access + " its data folder (permission denied)",
			"whoever runs Blerg needs to give the runner's user ownership of its data volume (see \"storage error\" in the install troubleshooting notes)"
	case errors.Is(err, fs.ErrNotExist):
		return "a file or folder it needs is missing", "whoever runs Blerg needs to check that the runner's data volume is mounted"
	default:
		return "of an unexpected storage error", "try again, and if it keeps happening give whoever runs Blerg this error id"
	}
}

// writeStorageFailure answers a failure of the runner's disk during step ("save the file",
// "read the file"), which needed access to the data folder: 500, the sentence for its kind, an
// error id.
func writeStorageFailure(w http.ResponseWriter, step, access, logContext string, err error) string {
	kind, todo := storageKind(err, access)
	return writeFailure(w, http.StatusInternalServerError, "The runner could not "+step+" because "+kind+"; "+todo+".", logContext, err)
}

// writeDatabaseFailure answers a failure of the runner's database during step.
func writeDatabaseFailure(w http.ResponseWriter, step, logContext string, err error) {
	writeFailure(w, http.StatusInternalServerError,
		"The runner could not "+step+" because its database did not answer; try again, and if it keeps happening give whoever runs Blerg this error id.",
		logContext, err)
}

// errWriter remembers the first error its writer returned, so a copy that failed can be told
// apart: the disk (a write) or the client (a read of the request body).
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) Write(p []byte) (int, error) {
	n, err := e.w.Write(p)
	if err != nil && e.err == nil {
		e.err = err
	}
	return n, err
}
