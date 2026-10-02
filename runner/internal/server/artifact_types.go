package server

import (
	"net/http"
	"path"
	"strings"
	"unicode/utf8"
)

// What the app can show of a published file. This is the ONE place that maps a
// file to a `view`; the browser keeps a matching table keyed by the same
// strings (frontend/src/lib/artifactViews.ts) and treats any view it does not
// know as download-only.
//
//	markdown  rendered by the chat's Markdown component (raw HTML is not run)
//	text      plain text / source / logs, monospace
//	json      pretty-printed text
//	csv       a table (csv and tsv)
//	image     png jpg gif webp svg, shown only through <img>
//	pdf       the browser's PDF viewer, from a blob URL
//	audio     <audio controls> from a blob URL
//	video     <video controls> from a blob URL
//	html      a sandboxed iframe (scripts, no same-origin, no network)
//	none      no preview: download only
//
// SECURITY: the content type the uploader sends is never used. The type stored
// and served comes from this table (by extension), or from sniffing the bytes
// when the extension is unknown. Nothing is ever served renderable from the app
// origin: see artifactServedType.
const (
	viewMarkdown = "markdown"
	viewText     = "text"
	viewJSON     = "json"
	viewCSV      = "csv"
	viewImage    = "image"
	viewPDF      = "pdf"
	viewAudio    = "audio"
	viewVideo    = "video"
	viewHTML     = "html"
	viewNone     = "none"

	octetStream = "application/octet-stream"
	// maxSniffText is the largest file of an unrecognised extension that is
	// offered as text when its bytes look like text.
	maxSniffText = 2 << 20
)

type artifactType struct{ contentType, view string }

var artifactByExt = buildArtifactExts()

func buildArtifactExts() map[string]artifactType {
	m := map[string]artifactType{}
	add := func(t artifactType, exts ...string) {
		for _, e := range exts {
			m[e] = t
		}
	}
	add(artifactType{"text/markdown; charset=utf-8", viewMarkdown}, "md", "markdown")
	add(artifactType{"text/plain; charset=utf-8", viewText},
		"txt", "log", "py", "js", "mjs", "cjs", "ts", "tsx", "jsx", "go", "rs", "java", "c", "h", "cpp", "cc", "hpp",
		"cs", "sh", "bash", "zsh", "yaml", "yml", "toml", "ini", "cfg", "conf", "sql", "css", "scss", "rb", "php",
		"swift", "kt", "lua", "pl", "r", "diff", "patch", "tex", "rst", "jsonl", "ndjson", "vue", "svelte", "gradle")
	add(artifactType{"application/xml", viewText}, "xml")
	add(artifactType{"application/json", viewJSON}, "json")
	add(artifactType{"text/csv; charset=utf-8", viewCSV}, "csv")
	add(artifactType{"text/tab-separated-values; charset=utf-8", viewCSV}, "tsv")
	add(artifactType{"image/png", viewImage}, "png")
	add(artifactType{"image/jpeg", viewImage}, "jpg", "jpeg")
	add(artifactType{"image/gif", viewImage}, "gif")
	add(artifactType{"image/webp", viewImage}, "webp")
	add(artifactType{"image/svg+xml", viewImage}, "svg")
	add(artifactType{"application/pdf", viewPDF}, "pdf")
	add(artifactType{"audio/mpeg", viewAudio}, "mp3")
	add(artifactType{"audio/wav", viewAudio}, "wav")
	add(artifactType{"audio/ogg", viewAudio}, "ogg")
	add(artifactType{"audio/mp4", viewAudio}, "m4a")
	add(artifactType{"video/mp4", viewVideo}, "mp4")
	add(artifactType{"video/webm", viewVideo}, "webm")
	add(artifactType{"text/html; charset=utf-8", viewHTML}, "html", "htm")
	add(artifactType{"application/zip", viewNone}, "zip")
	add(artifactType{"application/vnd.openxmlformats-officedocument.wordprocessingml.document", viewNone}, "docx")
	add(artifactType{"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", viewNone}, "xlsx")
	add(artifactType{"application/vnd.openxmlformats-officedocument.presentationml.presentation", viewNone}, "pptx")
	return m
}

// artifactByBase names extensionless files that are plain text.
var artifactByBase = map[string]bool{
	"dockerfile": true, "makefile": true, "readme": true, "license": true, "changelog": true, "gemfile": true,
}

// classifyArtifact decides the stored content type and the view of a file from
// its (sanitised) name and its first bytes. size is the whole file's size.
func classifyArtifact(name string, head []byte, size int64) (contentType, view string) {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	t, known := artifactByExt[ext]
	if !known && ext == "" && artifactByBase[strings.ToLower(name)] {
		t, known = artifactType{"text/plain; charset=utf-8", viewText}, true
	}
	sniffed := http.DetectContentType(head)
	if known {
		// A binary view is only believed when the bytes agree: a "png" that is
		// really something else is offered as a download, not shown.
		switch t.view {
		case viewImage:
			if t.contentType != "image/svg+xml" && !strings.HasPrefix(sniffed, t.contentType) {
				return octetStream, viewNone
			}
		case viewPDF:
			if sniffed != "application/pdf" {
				return octetStream, viewNone
			}
		}
		return t.contentType, t.view
	}
	switch {
	case strings.HasPrefix(sniffed, "image/png"), strings.HasPrefix(sniffed, "image/jpeg"),
		strings.HasPrefix(sniffed, "image/gif"), strings.HasPrefix(sniffed, "image/webp"):
		return strings.SplitN(sniffed, ";", 2)[0], viewImage
	case sniffed == "application/pdf":
		return sniffed, viewPDF
	case strings.HasPrefix(sniffed, "text/") && size <= maxSniffText && utf8.Valid(head):
		// Even HTML that arrives without a telling name is shown as text.
		return "text/plain; charset=utf-8", viewText
	}
	return octetStream, viewNone
}

// viewForContentType is the view of a stored content type. The table keeps the two in step (a
// test checks classifyArtifact against it), which is why the database stores only the type.
func viewForContentType(stored string) string {
	base := strings.ToLower(strings.TrimSpace(strings.SplitN(stored, ";", 2)[0]))
	switch {
	case base == "text/markdown":
		return viewMarkdown
	case base == "text/plain", base == "application/xml":
		return viewText
	case base == "application/json":
		return viewJSON
	case base == "text/csv", base == "text/tab-separated-values":
		return viewCSV
	case strings.HasPrefix(base, "image/"):
		return viewImage
	case base == "application/pdf":
		return viewPDF
	case strings.HasPrefix(base, "audio/"):
		return viewAudio
	case strings.HasPrefix(base, "video/"):
		return viewVideo
	case base == "text/html":
		return viewHTML
	}
	return viewNone
}

// artifactServedType is the Content-Type the /download and /raw routes send for
// a stored type. The bodies are agent-authored, and these routes live on the
// app origin, so nothing renderable is ever served with its own type:
//
//   - raw keeps png/jpeg/gif/webp and pdf (inert; needed by the viewer) and
//     sends everything else, SVG and HTML and XML included, as octet-stream;
//     the viewer puts SVG behind <img>, where script never runs.
//   - download adds the other non-renderable types (text, csv, json, media,
//     office, zip) because it is an attachment as well.
func artifactServedType(stored string, raw bool) string {
	base := strings.ToLower(strings.TrimSpace(strings.SplitN(stored, ";", 2)[0]))
	switch base {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf":
		return stored
	}
	if raw {
		return octetStream
	}
	switch {
	case base == "text/html", base == "image/svg+xml", base == "application/xml", base == "text/xml",
		strings.Contains(base, "javascript"), strings.Contains(base, "xhtml"):
		return octetStream
	case strings.HasPrefix(base, "text/"), base == "application/json", base == "application/zip",
		strings.HasPrefix(base, "audio/"), strings.HasPrefix(base, "video/"),
		strings.HasPrefix(base, "application/vnd.openxmlformats-officedocument."):
		return stored
	}
	return octetStream
}

// sanitizeArtifactName reduces an uploader's name to a safe file name: the base
// name only (either separator), no control characters, valid UTF-8, at most
// maxArtifactName bytes (the extension is kept when it fits), and "artifact"
// when nothing is left.
func sanitizeArtifactName(raw string) string {
	raw = strings.ToValidUTF8(raw, "")
	if i := strings.LastIndexAny(raw, `/\`); i >= 0 {
		raw = raw[i+1:]
	}
	raw = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0x202e || r == 0x2028 || r == 0x2029 {
			return -1
		}
		return r
	}, raw)
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "." || raw == ".." {
		return "artifact"
	}
	if len(raw) > maxArtifactName {
		ext := path.Ext(raw)
		if len(ext) > 20 {
			ext = ""
		}
		stem := cutUTF8(strings.TrimSuffix(raw, ext), maxArtifactName-len(ext))
		raw = stem + ext
		if strings.Trim(raw, ". ") == "" {
			return "artifact"
		}
	}
	return raw
}

// cutUTF8 truncates s to at most n bytes without splitting a rune.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
