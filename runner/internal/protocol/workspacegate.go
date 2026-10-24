package protocol

// WorkspaceRuntimeGateVerified is shared by both transport hops. Protocol
// support alone cannot advertise a runtime whose integration/platform/security
// acceptance is incomplete. There is no environment or administrator override.
const WorkspaceRuntimeGateVerified = false

func VerifiedWorkspaceCapabilities() WorkspaceCapabilities {
	if !RuntimeGateEnabled(WorkspaceRuntimeGateVerified) {
		return WorkspaceCapabilities{}
	}
	return WorkspaceCapabilities{ManifestVersion: WorkspaceVersion}
}

// RuntimeGateEnabled has a compile-time integration-test override only. No
// environment, wire field, administrator setting or linker variable enables it.
func RuntimeGateEnabled(verified bool) bool { return verified || workspaceIntegrationBuild }

// NegotiateWorkspaceCapabilities grants only versions understood by both hops.
// Unknown versions and shell without workspace v2 are never upgraded implicitly.
func NegotiateWorkspaceCapabilities(local, peer WorkspaceCapabilities) WorkspaceCapabilities {
	if local.ManifestVersion != WorkspaceVersion || peer.ManifestVersion != WorkspaceVersion {
		return WorkspaceCapabilities{}
	}
	result := WorkspaceCapabilities{ManifestVersion: WorkspaceVersion}
	if local.ShellVersion == 1 && peer.ShellVersion == 1 {
		result.ShellVersion = 1
	}
	return result
}
