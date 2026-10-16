package daemon

// Restricted configuration is pinned to the exact CLI whose model-visible
// tool catalog is exercised by TestCodexRestrictedRealCatalog. It never
// inherits user/project config or sends gateway credentials to the child.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const codexRestrictedVersion = "0.162.1"

// Every boolean feature present in this pinned runtime is disabled before
// startup, including shell, browser, delegation, code mode, plugins, hooks,
// tool discovery and native MCP/apps. Host skill discovery is then skipped.
var codexRestrictedDisabledFeatures = []string{
	"agent_message_board",
	"analytics_plan_history",
	"api_key_cyber_access_programs",
	"api_key_model_discovery",
	"apply_patch_freeform",
	"apply_patch_preserve_line_endings",
	"apply_patch_streaming_events",
	"apps",
	"apps_mcp_path_override",
	"auth_elicitation",
	"background_paginated_rollout_migration",
	"bedrock_setup_wizard",
	"browser_annotation_api",
	"browser_use",
	"browser_use_external",
	"browser_use_full_cdp_access",
	"chronicle",
	"cli_daybreak",
	"code_mode_buffered_exec",
	"code_mode_interrupt",
	"code_mode_only",
	"code_mode_only_strict_3p_tools",
	"code_mode_prewarm",
	"code_mode_tool_search",
	"codex_apps_mcp_2026_07_28",
	"codex_git_commit",
	"collaboration_modes",
	"compaction_image_budget",
	"computer_use",
	"concurrent_reasoning_summaries",
	"content_item_kinds",
	"cwd_relative_turn_diffs",
	"daemon_auto_start",
	"default_mode_request_user_input",
	"defer_mailbox_preemption",
	"deferred_executor",
	"deferred_tool_world_state",
	"elevated_windows_sandbox",
	"enable_fanout",
	"enable_mcp_apps",
	"enable_request_compression",
	"exec_permission_approvals",
	"executed_tool_call_metadata",
	"executor_capability_discovery",
	"experimental_windows_sandbox",
	"external_agent_memory_import",
	"external_migration",
	"fast_mode",
	"goals",
	"guardian_approval",
	"guardian_conversation_history_tools",
	"guardian_enhanced_node_repl_transcripts",
	"guardian_ext",
	"guardian_node_repl_transcript_images",
	"guardian_reuse_parent_compaction",
	"guardian_root_handoff_context",
	"guardianv2_decisions_comparison",
	"hooks",
	"image_detail_original",
	"image_generation",
	"image_resize_notice",
	"in_app_browser",
	"in_app_chat",
	"in_app_dictation",
	"in_app_local_automation",
	"in_app_updates",
	"in_app_voice",
	"incremental_tools",
	"instant_interrupt",
	"item_ids",
	"js_repl",
	"js_repl_tools_only",
	"local_thread_store_compression",
	"local_thread_store_shared_compression",
	"login_shell_package_path",
	"mcp_2026_07_28",
	"mcp_oauth_refresh_coordination",
	"memories",
	"mentions_v2",
	"model_catalog_in_context",
	"multi_agent",
	"multi_agent_mode",
	"multi_agent_v2_dynamic_tools",
	"nonfatal_clock_read_errors",
	"omit_app_server_notification_media",
	"personality",
	"plugin_hooks",
	"plugin_sharing",
	"plugins",
	"powershell_shell_version",
	"prefer_mxc",
	"prevent_idle_sleep",
	"psp",
	"realtime_conversation",
	"reasoning_effort_override",
	"recommended_plugins",
	"remote_compaction_v2",
	"remote_control",
	"remote_models",
	"remote_plugin",
	"request_permissions_tool",
	"request_rule",
	"resize_all_images",
	"respect_system_proxy",
	"responses_websockets",
	"responses_websockets_v2",
	"retain_client_developer_messages",
	"runtime_metrics",
	"search_tool",
	"secret_auth_storage",
	"send_async_message",
	"send_message_to_user_async",
	"shell_snapshot",
	"shell_snapshot_v2",
	"shell_tool",
	"shell_zsh_fork",
	"skill_env_var_dependency_prompt",
	"skill_mcp_dependency_install",
	"skill_search",
	"skip_host_skill_discovery",
	"sqlite",
	"stable_environment_tools",
	"standalone_web_search",
	"steer",
	"step_model_switching",
	"system_proxy_fallback",
	"terminal_resize_reflow",
	"terminal_visualization_instructions",
	"tool_call_mcp_elicitation",
	"tool_search",
	"tool_search_always_defer_mcp_tools",
	"tool_suggest",
	"transcript_v2",
	"tui_app_server",
	"ultrafast_mode",
	"unavailable_dummy_tools",
	"unbounded_connection_retries",
	"undo",
	"unified_exec",
	"unified_exec_tty",
	"unified_exec_zsh_fork",
	"unified_image_budget",
	"use_agent_identity",
	"use_legacy_landlock",
	"use_linux_sandbox_bwrap",
	"use_xaa",
	"view_image",
	"web_search_cached",
	"web_search_request",
	"windows_sandbox_service",
	"workspace_dependencies",
	"workspace_owner_usage_nudge",
	"worktrees",
	"write_stdin_approval",
}

func codexRestrictedConfig(model, provider string, providers map[string]any) map[string]any {
	c := map[string]any{"web_search": "disabled", "project_doc_max_bytes": 0, "sandbox_mode": "read-only", "approval_policy": "never", "tools.experimental_request_user_input.enabled": false, "tools.update_plan.enabled": false, "agents.enabled": false, "code_mode.enabled": false, "skills.include_instructions": false, "mcp_servers": map[string]any{}, "plugins": map[string]any{}}
	for _, feature := range codexRestrictedDisabledFeatures {
		c["features."+feature] = false
	}
	c["features.skip_host_skill_discovery"] = true
	if model != "" {
		c["model"] = model
	}
	if provider != "" {
		c["model_provider"] = provider
	}
	if providers != nil {
		c["model_providers"] = providers
	}
	return c
}

func codexRestrictedVersionCheck(ctx context.Context, binary string) error {
	output, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil || strings.TrimSpace(string(output)) != "codex-cli "+codexRestrictedVersion {
		return fmt.Errorf("restricted Codex sessions require the verified Codex CLI %s", codexRestrictedVersion)
	}
	return nil
}

// TOML quoted keys preserve arbitrary provider map entries as data. This
// file contains only non-secret policy; login is a separate private link.
func codexRestrictedConfigBytes(config map[string]any) ([]byte, error) {
	keys := make([]string, 0, len(config))
	for k := range config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out strings.Builder
	for _, key := range keys {
		if strings.Contains(key, "\n") {
			return nil, errors.New("invalid restricted configuration key")
		}
		value := config[key]
		if m, ok := value.(map[string]any); ok {
			if len(m) == 0 {
				out.WriteString(key + "={}\n")
				continue
			}
			encoded, err := codexInlineTOML(m)
			if err != nil {
				return nil, err
			}
			out.WriteString(key + "=" + encoded + "\n")
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, errors.New("invalid restricted configuration value")
		}
		out.WriteString(key + "=" + string(raw) + "\n")
	}
	return []byte(out.String()), nil
}
func codexInlineTOML(m map[string]any) (string, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	values := []string{}
	for _, k := range keys {
		key, _ := json.Marshal(k)
		var value string
		if child, ok := m[k].(map[string]any); ok {
			var err error
			value, err = codexInlineTOML(child)
			if err != nil {
				return "", err
			}
		} else {
			raw, err := json.Marshal(m[k])
			if err != nil {
				return "", errors.New("invalid restricted provider value")
			}
			value = string(raw)
		}
		values = append(values, string(key)+"="+value)
	}
	return "{" + strings.Join(values, ",") + "}", nil
}

func writeCodexRestrictedConfig(home string, config map[string]any) error {
	body, err := codexRestrictedConfigBytes(config)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(home, "config.toml"), body, 0600)
}
