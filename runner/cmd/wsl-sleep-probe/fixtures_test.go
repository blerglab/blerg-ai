package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestReportRecordedFixtures(t *testing.T) {
	for _, name := range []string{"gaps", "single"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile("testdata/" + name + ".jsonl")
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err = report(bytes.NewReader(data), &out)
			if name == "single" {
				if err == nil || !strings.Contains(err.Error(), "at least two samples") {
					t.Fatalf("want insufficient evidence error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile("testdata/gaps.tsv")
			if err != nil {
				t.Fatal(err)
			}
			if out.String() != string(want) {
				t.Fatalf("report mismatch:\n%s", out.String())
			}
		})
	}
}
