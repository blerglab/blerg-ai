package main

import (
	"github.com/blerglab/blerg-ai/runner/internal/testsupport"
	"os"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(testsupport.Run(m)) }
