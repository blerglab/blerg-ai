package releaseperf

import "testing"

func TestValidateVersionAccepts(t *testing.T) {
	for _, v := range []string{"dev", "0.6.0", "v0.6.0", "0.6.0-rc1", "0.6.0.candidate"} {
		if err := ValidateVersion(v); err != nil {
			t.Errorf("ValidateVersion(%q) = %v, want nil", v, err)
		}
	}
}

func TestValidateVersionRejects(t *testing.T) {
	for _, v := range []string{
		"", "../../etc/passwd", "0.6.0/../../x", "a/b", `a\b`, "0.6.0 ", "0.6.0;rm -rf", "..",
	} {
		if err := ValidateVersion(v); err == nil {
			t.Errorf("ValidateVersion(%q) = nil, want an error", v)
		}
	}
}
