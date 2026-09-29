package agentsmanifest

import (
	"net/http"
	"strings"
)

// WantsMarkdown reports whether the request asked for the Markdown rendering of
// a manifest, either with `Accept: text/markdown` or with `?format=md`.
func WantsMarkdown(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.URL != nil && r.URL.Query().Get("format") == "md" {
		return true
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/markdown")
}

// RenderComponentMarkdown renders one component's manifest as the Markdown an
// LLM reads: title, description, Authenticate, Operations, Machine-readable.
func RenderComponentMarkdown(e ComponentEntry) string {
	var b strings.Builder
	writeComponent(&b, e, 1)
	return b.String()
}

// RenderAggregateMarkdown renders the whole install as the "getting started"
// document: the quickstart first, then one section per component.
func RenderAggregateMarkdown(m AggregateManifest) string {
	var b strings.Builder
	b.WriteString("# Blerg — agent guide\n\n")
	if m.CoreBaseURL != "" {
		b.WriteString("Core: " + m.CoreBaseURL + "\n")
	}
	if m.ContractVersion != "" {
		b.WriteString("Contract version: `" + m.ContractVersion + "`\n")
	}
	if m.CoreBaseURL != "" || m.ContractVersion != "" {
		b.WriteString("\n")
	}
	if strings.TrimSpace(m.Quickstart) != "" {
		b.WriteString("## Quickstart\n\n")
		b.WriteString(strings.TrimRight(m.Quickstart, "\n"))
		b.WriteString("\n\n")
	}
	for _, c := range m.Components {
		writeComponent(&b, c, 2)
	}
	return b.String()
}

// writeComponent renders one component with its title at the given heading
// level (1 standalone, 2 inside the aggregate guide).
func writeComponent(b *strings.Builder, e ComponentEntry, level int) {
	h := strings.Repeat("#", level)
	sub := strings.Repeat("#", level+1)

	b.WriteString(h + " " + e.Name + "\n\n")
	if e.Description != "" {
		b.WriteString(e.Description + "\n\n")
	}
	if e.BaseURL != "" {
		b.WriteString("Base URL: " + e.BaseURL + "\n")
	}
	if e.Version != "" {
		b.WriteString("Version: " + e.Version + "\n")
	}
	if e.ContractVersion != "" {
		b.WriteString("Contract version: `" + e.ContractVersion + "`\n")
	}
	if len(e.Capabilities) > 0 {
		b.WriteString("Capabilities: " + codeList(e.Capabilities) + "\n")
	}
	if e.BaseURL != "" || e.Version != "" || e.ContractVersion != "" || len(e.Capabilities) > 0 {
		b.WriteString("\n")
	}

	if e.Auth != nil {
		b.WriteString(sub + " Authenticate\n\n")
		b.WriteString("- Audience: `" + e.Auth.Audience + "`\n")
		if len(e.Auth.Accepts) > 0 {
			b.WriteString("- Accepts: " + codeList(e.Auth.Accepts) + "\n")
		}
		if len(e.Auth.Presets) > 0 {
			b.WriteString("- Token presets: " + codeList(e.Auth.Presets) + "\n")
		}
		if e.Auth.TokenEndpoint != "" {
			b.WriteString("- Get a token: `POST " + e.Auth.TokenEndpoint + "` (or Settings → Agent tokens)\n")
		}
		b.WriteString("\nSend it as `Authorization: Bearer <token>`.\n\n")
	}

	if len(e.Operations) > 0 {
		b.WriteString(sub + " Operations\n\n")
		b.WriteString("| Method | Path | Summary | Cap | Idempotent |\n")
		b.WriteString("| --- | --- | --- | --- | --- |\n")
		for _, op := range e.Operations {
			capCell := "—"
			if op.Cap != "" {
				capCell = "`" + cell(op.Cap) + "`"
			}
			idem := "no"
			if op.Idempotent {
				idem = "yes"
			}
			b.WriteString("| " + cell(op.Method) +
				" | `" + cell(op.Path) +
				"` | " + cell(op.Summary) +
				" | " + capCell +
				" | " + idem + " |\n")
		}
		b.WriteString("\n")
	}

	if e.OpenAPIURL != "" || e.MCPURL != "" || e.DocsURL != "" {
		b.WriteString(sub + " Machine-readable\n\n")
		if e.DocsURL != "" {
			b.WriteString("- Manifest: " + e.DocsURL + "\n")
		}
		if e.OpenAPIURL != "" {
			b.WriteString("- OpenAPI 3.1: " + e.OpenAPIURL + "\n")
		}
		if e.MCPURL != "" {
			b.WriteString("- MCP: " + e.MCPURL + "\n")
		}
		b.WriteString("\n")
	}
}

// codeList renders values as a comma-separated list of inline code spans.
func codeList(values []string) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, "`"+cell(v)+"`")
	}
	return strings.Join(parts, ", ")
}

// cell makes a value safe inside a Markdown table cell.
func cell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
