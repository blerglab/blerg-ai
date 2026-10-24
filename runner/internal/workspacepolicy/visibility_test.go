package workspacepolicy

import "testing"

func TestVisibleBoundaries(t *testing.T) {
	for _, name := range []string{".ssh/config", "ordinary/.git/HEAD", "ordinary/.env", ".aws/public", "ordinary/credentials.private", "ordinary/.env/settings"} {
		if Directory(name) || File(name) {
			t.Errorf("protected path visible: %s", name)
		}
	}
	for _, name := range []string{"secret-project", "secret-project/docs", "secret-project/README.md"} {
		if !Directory(name) {
			t.Errorf("benign ancestor hidden: %s", name)
		}
	}
	if !File("secret-project/README.md") || File("ordinary/secret.txt") {
		t.Fatal("file versus ancestor policy changed")
	}
}
