package server

import (
	"fmt"
	"os"
)

// StorageProblem checks, once at startup, that the runner can write to its data directory
// (BLERG_RUNNER_DATA_DIR) and to the artifacts directory inside it, where published files and
// uploaded attachments are kept. It returns "" when both are writable, otherwise one plain
// sentence naming the directory, the user the runner runs as, the error and how to fix it on a
// desktop install and on Kubernetes. The caller logs it; the runner still starts, because
// everything that does not store a file works without the directory.
//
// The usual cause is a volume whose owner is not the runner's user (the image runs as the
// unprivileged user 65532), which makes every publish and upload fail with "storage error".
func StorageProblem() string {
	dataDir := publishDataDir("")
	for _, dir := range []string{dataDir, artifactsDir()} {
		if err := probeWritable(dir); err != nil {
			return storageProblemMessage(dir, os.Getuid(), err)
		}
	}
	return ""
}

// probeWritable makes sure dir exists (the runner would create it on first use anyway) and
// that a file can be created and removed in it.
func probeWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".write-check-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

func storageProblemMessage(dir string, uid int, err error) string {
	return fmt.Sprintf("STORAGE NOT WRITABLE: the runner (user id %d) cannot write to %q: %v. "+
		"Publishing files, attachments and uploads will fail with \"storage error\" until this is fixed; everything else works. "+
		"Desktop: run ./blerg-up.sh again in install/desktop (it repairs the owner of the runner's data volume), "+
		"or see \"storage error\" in install/desktop/DAEMON.md. "+
		"Kubernetes: make the runner's data volume writable by user id 65532 (the pod's fsGroup: 65532 does this for most storage classes; "+
		"otherwise chown the volume to 65532).",
		uid, dir, err)
}
