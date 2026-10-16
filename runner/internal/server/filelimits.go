package server

// What a session's files may weigh, and how the runner tells its clients.
//
// One file, whoever sent it (an agent's `blerg-runner publish`, a person's attachment), is at most
// maxArtifactBytes. A session's attachments together are at most maxUploadBytesPerSession, and the
// files its agent published together at most maxArtifactBytesPerSession. The file counts
// (maxArtifactsPerSession, maxUploadsPerSession) are not configurable.
//
// The sizes are set once at start from the environment (ConfigureFileLimits) and reported to
// every client that sends files, so a client can refuse a file that is too big before sending it:
// a server that answers a large body without reading it closes the connection, and the client
// then sees a reset instead of the 413. The limits travel as `limits` in GET
// /api/sessions/{id}/files (the CLI), GET /api/runner/sessions/{id} (the chat proxy) and the
// browser's initial_state. A client that finds none (an older runner) assumes 25 MiB.

import (
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const (
	defaultMaxFileMB             = 100
	defaultMaxSessionUploadsMB   = 500
	defaultMaxSessionArtifactsMB = 1024
	// maxConfigMB bounds a configured size (1 TiB), so the byte count cannot overflow.
	maxConfigMB = 1 << 20
)

// Limits. Vars rather than consts so a test can exercise them without moving 100 MiB.
var (
	maxArtifactBytes           int64 = defaultMaxFileMB << 20
	maxArtifactBytesPerSession int64 = defaultMaxSessionArtifactsMB << 20
	maxUploadBytesPerSession   int64 = defaultMaxSessionUploadsMB << 20
	maxArtifactsPerSession           = 50
	maxUploadsPerSession             = 100
)

// ConfigureFileLimits reads BLERG_RUNNER_MAX_FILE_MB, BLERG_RUNNER_MAX_SESSION_UPLOADS_MB and
// BLERG_RUNNER_MAX_SESSION_ARTIFACTS_MB through getenv (whole MiB). An unset or empty variable
// keeps the default; an invalid one keeps it too and logs one line saying so. A per-file limit
// above either session total is lowered to it, with one log line.
func ConfigureFileLimits(getenv func(string) string) {
	maxArtifactBytes = envMiB(getenv, "BLERG_RUNNER_MAX_FILE_MB", defaultMaxFileMB)
	maxUploadBytesPerSession = envMiB(getenv, "BLERG_RUNNER_MAX_SESSION_UPLOADS_MB", defaultMaxSessionUploadsMB)
	maxArtifactBytesPerSession = envMiB(getenv, "BLERG_RUNNER_MAX_SESSION_ARTIFACTS_MB", defaultMaxSessionArtifactsMB)
	// One file can never be larger than a session may hold: a client that checked the advertised
	// per-file limit would otherwise send a file the total always refuses.
	if total := min(maxUploadBytesPerSession, maxArtifactBytesPerSession); maxArtifactBytes > total {
		log.Printf("config: the file limit (%d MiB, BLERG_RUNNER_MAX_FILE_MB) is larger than a session total (attachments %d, published %d MiB); one file is limited to %d MiB",
			maxArtifactBytes>>20, maxUploadBytesPerSession>>20, maxArtifactBytesPerSession>>20, total>>20)
		maxArtifactBytes = total
	}
}

func envMiB(getenv func(string) string, name string, def int64) int64 {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return def << 20
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 1 || n > maxConfigMB {
		shown := raw
		if len(shown) > 40 {
			shown = shown[:40] + "..."
		}
		log.Printf("config: %s=%q is not a whole number of MiB from 1 to %d; using the default, %d MiB", name, shown, maxConfigMB, def)
		return def << 20
	}
	return n << 20
}

// currentFileLimits is what the runner advertises (the `limits` object).
func currentFileLimits() protocol.FileLimits {
	return protocol.FileLimits{
		MaxFileBytes:            maxArtifactBytes,
		MaxSessionUploadBytes:   maxUploadBytesPerSession,
		MaxSessionArtifactBytes: maxArtifactBytesPerSession,
	}
}

// sizeText is a byte count as people read it: whole MiB when it is one ("100 MiB"), else one
// decimal rounded UP ("130.5 MiB"), so a file one byte over a limit never reads as the limit
// itself; KiB or bytes below a MiB.
func sizeText(n int64) string {
	up := func(x float64) float64 { return math.Ceil(x*10) / 10 }
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", up(float64(n)/(1<<20)))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", up(float64(n)/(1<<10)))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}

// tooLargeMsg is the 413's sentence: the file's size when the request said it, and the limit.
func tooLargeMsg(size int64) string {
	if size > 0 {
		return fmt.Sprintf("This file is %s; the runner takes at most %s per file. Send a smaller file, or split it.", sizeText(size), sizeText(maxArtifactBytes))
	}
	return fmt.Sprintf("This file is larger than the runner's limit of %s per file. Send a smaller file, or split it.", sizeText(maxArtifactBytes))
}
