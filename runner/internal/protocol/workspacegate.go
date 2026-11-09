package protocol

// Release policy is explicit for each runtime. WSL runs the Linux backend.
// Cluster workspace and shell support passed the real-pod happy-path smoke.
// macOS host containment remains unavailable (#181). Runtime readiness and
// exact peer negotiation are required independently of these release switches.
// No environment variable or browser field can enable a disabled runtime.
const LinuxHostWorkspaceEnabled = true
const SandboxWorkspaceEnabled = true
const MacOSHostWorkspaceEnabled = false
const ClusterWorkspaceEnabled = true

func WorkspaceRuntimeEnabled(runtime, platform string) bool {
	switch runtime {
	case "host":
		switch platform {
		case "linux":
			return LinuxHostWorkspaceEnabled
		case "darwin":
			return MacOSHostWorkspaceEnabled
		}
	case "sandbox":
		return SandboxWorkspaceEnabled && (platform == "linux" || platform == "darwin")
	case "cluster":
		return platform == "linux" && (ClusterWorkspaceEnabled || workspaceIntegrationBuild)
	}
	return false
}

// EnabledWorkspaceCapabilities is server policy, not proof of an installed
// backend. Peers must advertise their verified subset; negotiation intersects
// each runtime independently. Shell always follows the same runtime switches.
func EnabledWorkspaceCapabilities() WorkspaceCapabilities {
	c := WorkspaceCapabilities{LinuxHost: WorkspaceRuntimeEnabled("host", "linux"), MacOSHost: WorkspaceRuntimeEnabled("host", "darwin"), Sandbox: WorkspaceRuntimeEnabled("sandbox", "linux"), Cluster: WorkspaceRuntimeEnabled("cluster", "linux")}
	if c.LinuxHost || c.MacOSHost || c.Sandbox || c.Cluster {
		c.ManifestVersion = WorkspaceVersion
		c.ShellVersion = 1
		c.Membership = WorkspaceMembershipVersion
	}
	return c
}

func WorkspaceCapabilitiesFor(runtime, platform string) WorkspaceCapabilities {
	if !WorkspaceRuntimeEnabled(runtime, platform) {
		return WorkspaceCapabilities{}
	}
	c := WorkspaceCapabilities{ManifestVersion: WorkspaceVersion, ShellVersion: 1}
	switch runtime {
	case "host":
		c.LinuxHost = platform == "linux"
		c.MacOSHost = platform == "darwin"
		c.Membership = WorkspaceMembershipVersion
	case "sandbox":
		c.Sandbox = true
		c.Membership = WorkspaceMembershipVersion
	case "cluster":
		c.Cluster = true
	}
	return c
}

// Supports requires affirmative runtime support. A legacy version-only peer is
// not upgraded implicitly, even if another runtime on the connection is ready.
func (c WorkspaceCapabilities) Supports(runtime string, shell bool) bool {
	if c.ManifestVersion != WorkspaceVersion || shell && c.ShellVersion != 1 {
		return false
	}
	switch runtime {
	case "host":
		return c.LinuxHost || c.MacOSHost
	case "sandbox":
		return c.Sandbox
	case "cluster":
		return c.Cluster
	}
	return false
}

func NegotiateWorkspaceCapabilities(local, peer WorkspaceCapabilities) WorkspaceCapabilities {
	if local.ManifestVersion != WorkspaceVersion || peer.ManifestVersion != WorkspaceVersion {
		return WorkspaceCapabilities{}
	}
	c := WorkspaceCapabilities{LinuxHost: local.LinuxHost && peer.LinuxHost, MacOSHost: local.MacOSHost && peer.MacOSHost, Sandbox: local.Sandbox && peer.Sandbox, Cluster: local.Cluster && peer.Cluster}
	if !c.LinuxHost && !c.MacOSHost && !c.Sandbox && !c.Cluster {
		return WorkspaceCapabilities{}
	}
	c.ManifestVersion = WorkspaceVersion
	if local.ShellVersion == 1 && peer.ShellVersion == 1 {
		c.ShellVersion = 1
	}
	if local.Membership == WorkspaceMembershipVersion && peer.Membership == WorkspaceMembershipVersion {
		c.Membership = WorkspaceMembershipVersion
	}
	if c.ShellVersion == 1 && local.ShellPersistence == ShellPersistenceVersion && peer.ShellPersistence == ShellPersistenceVersion {
		c.PersistentHost = c.LinuxHost && local.PersistentHost && peer.PersistentHost
		// Sandbox v2 is disabled: docker exec exposes bootstrap pipes to the agent UID.
		c.PersistentSandbox = false
		if c.PersistentHost || c.PersistentSandbox {
			c.ShellPersistence = ShellPersistenceVersion
		}
	}
	return c
}

func (c WorkspaceCapabilities) SupportsPersistence(runtime string) bool {
	if c.ShellPersistence != ShellPersistenceVersion || !c.Supports(runtime, true) {
		return false
	}
	switch runtime {
	case "host":
		return c.LinuxHost && c.PersistentHost
	case "sandbox":
		return false
	}
	return false
}
