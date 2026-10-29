package workspacepolicy

import (
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"path/filepath"
	"strings"
)

// FoldName normalizes Unicode canonically and folds case for public path
// comparisons, including protected names and case-insensitive filesystems.
func FoldName(name string) string { // Retain the conservative upper/lower comparison used by the protected-name
	// policy, including Unicode variants that can map to ASCII in one direction.
	// ASCII names (nearly all) fold to their lower case: normalization and case folding change
	// nothing else there, and this runs for every segment of every listed path.
	if isASCII(name) {
		return strings.ToLower(name)
	}
	return foldUnicode(name)
}

func foldUnicode(name string) string {
	return norm.NFC.String(cases.Fold().String(strings.ToLower(strings.ToUpper(norm.NFC.String(name)))))
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func workspaceSecretDirectory(name string) bool {
	switch FoldName(name) {
	case ".git", ".ssh", ".aws", ".gnupg", ".kube", ".docker", ".claude", ".codex", "secrets", ".vault":
		return true
	default:
		return false
	}
}

func workspaceSensitiveName(name string, substring bool) bool {
	name = FoldName(name)
	switch name {
	case ".env", ".netrc", ".npmrc", ".pypirc", ".git-credentials", ".pgpass", ".htpasswd", ".vault-token",
		"kubeconfig", ".kubeconfig":
		return true
	}
	for _, prefix := range []string{".env.", "id_rsa", "id_ed25519", "id_ecdsa", "id_dsa", "credentials"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	if substring && strings.Contains(name, "secret") {
		return true
	}
	for _, suffix := range []string{".pem", ".key", ".p12", ".pfx", ".kdbx", ".ppk", ".jks", ".keystore", ".tfstate", ".tfstate.backup"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// Apply protected-name checks to every component, before opening or disclosing
// metadata. A protected file-like name used as a directory remains protected.
func File(name string) bool {
	parts := strings.Split(filepath.ToSlash(name), "/")
	for i, part := range parts {
		if workspaceSecretDirectory(part) || workspaceSensitiveName(part, i == len(parts)-1) {
			return false
		}
	}
	return true
}

func Directory(name string) bool {
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if workspaceSecretDirectory(part) || workspaceSensitiveName(part, false) {
			return false
		}
	}
	return true
}
