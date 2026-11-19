package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestTestReportDispatch(t *testing.T) {
	t.Setenv("BLERG_RUNNER_SERVER_HTTP", "")
	t.Setenv("BLERG_RUNNER_SESSION_ID", "")
	t.Setenv("BLERG_RUNNER_SESSION_TOKEN", "")
	var out, stderr bytes.Buffer
	if code := runCommand([]string{"test", "report", "--help"}, &out, &stderr); code != 0 || !strings.Contains(out.String(), "usage: blerg-runner test report") {
		t.Fatalf("sandbox helper did not route test reports: code=%d out=%q stderr=%q", code, out.String(), stderr.String())
	}
}
