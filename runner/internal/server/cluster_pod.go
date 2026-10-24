package server

// The trusted runner and its tracers use a distinct non-root UID. Only the
// engine/PTY exec drops to agent UID 1000. Private scratch is a separate mount
// owned 0700 by UID 1001; it is never part of HOME or the retained projects PVC.
// This pod needs a namespace that permits the listed capabilities; it does not
// satisfy the Kubernetes Restricted Pod Security profile. No service account
// token, host namespace, hostPath, privileged container or exec API is used.
func isolatedClusterPod(pod map[string]any, container map[string]any) {
	image := container["image"]
	pod["securityContext"] = map[string]any{"fsGroup": 1000, "fsGroupChangePolicy": "OnRootMismatch"}
	// The fixed setpriv entry point installs ambient capabilities while dropping
	// to UID 1001 before the Go runner starts. OCI non-root cap-add alone does
	// not reliably survive exec on all runtimes (including Docker).
	container["command"] = []string{"/usr/bin/setpriv", "--reuid=1001", "--regid=1000", "--clear-groups", "--inh-caps=+chown,+dac_override,+setuid,+setgid,+kill,+sys_ptrace", "--ambient-caps=+chown,+dac_override,+setuid,+setgid,+kill,+sys_ptrace", "/usr/local/bin/blerg-runner-pod"}
	container["securityContext"] = map[string]any{"runAsNonRoot": false, "runAsUser": 0, "runAsGroup": 0, "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}, "add": []string{"CHOWN", "DAC_OVERRIDE", "SETUID", "SETGID", "KILL", "SYS_PTRACE"}}, "seccompProfile": map[string]any{"type": "RuntimeDefault"}}
	mounts, _ := container["volumeMounts"].([]map[string]any)
	container["volumeMounts"] = append(mounts, map[string]any{"name": "workspace-home", "mountPath": "/workspace"}, map[string]any{"name": "workspace-private", "mountPath": "/run/blerg-workspace-private"}, map[string]any{"name": "workspace-tmp", "mountPath": "/tmp"})
	volumes, _ := pod["volumes"].([]map[string]any)
	pod["volumes"] = append(volumes, map[string]any{"name": "workspace-home", "emptyDir": map[string]any{"sizeLimit": "4Gi"}}, map[string]any{"name": "workspace-private", "emptyDir": map[string]any{"sizeLimit": "1280Mi"}}, map[string]any{"name": "workspace-tmp", "emptyDir": map[string]any{"sizeLimit": "512Mi"}})
	pod["initContainers"] = []map[string]any{{"name": "workspace-storage", "image": image, "command": []string{"/bin/sh", "-ec", `cp -a /workspace/. /runtime-home/
chown -R 1000:1000 /runtime-home
chown 1001:1000 /runtime-home
chmod 0770 /runtime-home
chown 1001:1001 /runtime-private
chmod 0700 /runtime-private`}, "securityContext": map[string]any{"runAsUser": 0, "runAsNonRoot": false, "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}, "add": []string{"CHOWN", "DAC_OVERRIDE", "FOWNER"}}, "seccompProfile": map[string]any{"type": "RuntimeDefault"}}, "resources": map[string]any{"requests": map[string]string{"cpu": "10m", "memory": "32Mi"}, "limits": map[string]string{"cpu": "1", "memory": "128Mi"}}, "volumeMounts": []map[string]any{{"name": "workspace-home", "mountPath": "/runtime-home"}, {"name": "workspace-private", "mountPath": "/runtime-private"}}}}
}
