package sessionstorage

import "testing"

func TestFromEnvSeedsTheDefaultAndKeepsTheMaximumAboveIt(t *testing.T) {
	for _, tc := range []struct {
		env      string
		def, max int64
	}{{"", 500, 5000}, {"800", 800, 5000}, {"9000", 9000, 9000}, {"nope", 500, 5000}, {"0", 500, 5000}} {
		t.Setenv(UploadEnv, tc.env)
		if got := FromEnv(); got.DefaultUploadMB != tc.def || got.MaxUploadMB != tc.max {
			t.Errorf("%q: got %+v, want default %d max %d", tc.env, got, tc.def, tc.max)
		}
	}
}

func TestValidateRefusesAMaximumBelowTheDefault(t *testing.T) {
	if err := (Limits{DefaultUploadMB: 600, MaxUploadMB: 500}).Validate(); err == nil {
		t.Fatal("a maximum below the default was accepted")
	}
	if err := (Limits{DefaultUploadMB: 0, MaxUploadMB: 500}).Validate(); err == nil {
		t.Fatal("a zero default was accepted")
	}
	if err := (Limits{DefaultUploadMB: 500, MaxUploadMB: 500}).Validate(); err != nil {
		t.Fatal(err)
	}
}
