package workspacepolicy

import (
	"path/filepath"
	"strings"
)

// Fold both ways to catch Unicode letters that map to ASCII in one direction.
func foldName(name string) string { return strings.ToLower(strings.ToUpper(name)) }

func workspaceSecretDirectory(name string) bool {
	switch foldName(name) {
	case ".git", ".ssh", ".aws", ".gnupg", ".kube", ".docker", ".claude", ".codex", "secrets", ".vault":
		return true
	default:
		return false
	}
}

func workspaceSensitiveName(name string, substring bool) bool {
	name = foldName(name)
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
