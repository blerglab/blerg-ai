package secrets

import (
	"strings"
	"testing"
)

func TestCheckAndRequire(t *testing.T) {
	good := "0123456789abcdef0123456789abcdef"
	cases := []struct {
		value    string
		checkErr string // substring or "" for nil
		reqErr   string
	}{
		{"", "", "unset"},
		{good, "", ""},
		{"change-me-daemon-token", "placeholder", "placeholder"},
		{"CHANGEME", "placeholder", "placeholder"},
		{"ChangeMe1234567890", "placeholder", "placeholder"},
		{"password", "placeholder", "placeholder"},
		{"short", "16", "16"},
		{"exactly16bytes!!", "", ""},
	}
	for _, c := range cases {
		if err := Check("BLERG_X", c.value); (err == nil) != (c.checkErr == "") || (err != nil && !strings.Contains(err.Error(), c.checkErr)) {
			t.Errorf("Check(%q) = %v, want %q", c.value, err, c.checkErr)
		}
		if err := Require("BLERG_X", c.value); (err == nil) != (c.reqErr == "") || (err != nil && !strings.Contains(err.Error(), c.reqErr)) {
			t.Errorf("Require(%q) = %v, want %q", c.value, err, c.reqErr)
		}
		for _, err := range []error{Check("BLERG_X", c.value), Require("BLERG_X", c.value)} {
			if err != nil && c.value != "" && strings.Contains(err.Error(), c.value) {
				t.Errorf("error must not echo the value: %v", err)
			}
			if err != nil && !strings.Contains(err.Error(), "BLERG_X") {
				t.Errorf("error must name the variable: %v", err)
			}
		}
	}
}
