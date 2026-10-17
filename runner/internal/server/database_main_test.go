package server

import (
	"os"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/testsupport"
)

func TestMain(m *testing.M) { os.Exit(testsupport.Run(m)) }
