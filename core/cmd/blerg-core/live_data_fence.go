package main

import (
	"context"
	"flag"
	"io"
	"os"
	"time"

	"github.com/blerglab/blerg-ai/core/internal/livedata"
)

// An offline operator command, deliberately absent from all HTTP routers.
// Stop dispatch before restoring a database, fence retained external authority,
// then restart Core. Neither a restart nor an environment flag is a fence.
func runLiveDataFence(args []string) error {
	flags := flag.NewFlagSet("live-data-fence", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	reason := flags.String("reason", "", "restore, compromise, or operator")
	if flags.Parse(args) != nil || flags.NArg() != 0 || (*reason != "restore" && *reason != "compromise" && *reason != "operator") {
		return livedata.ErrUnavailable
	}
	if os.Getenv("BLERG_LIVE_DATA_AUTHORITY_BACKEND") == "kubernetes" {
		backend, err := livedata.InClusterAuthority()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err = backend.Fence(ctx)
		return err
	}
	_, err := livedata.FenceAuthority(os.Getenv("BLERG_LIVE_DATA_AUTHORITY_DIR"))
	return err
}
