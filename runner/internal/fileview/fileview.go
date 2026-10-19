package fileview

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
//	diff      a unified diff or patch (.diff, .patch): the diff viewer, file by file
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
// origin: the caller must apply its serving policy.
const (
	Markdown = "markdown"
	Text     = "text"
	Diff     = "diff"
	JSON     = "json"
	CSV      = "csv"
	Image    = "image"
	PDF      = "pdf"
	Audio    = "audio"
	Video    = "video"
	HTML     = "html"
	None     = "none"

	OctetStream = "application/octet-stream"
	// MaxSniffText is the largest file of an unrecognised extension that is
	// offered as text when its bytes look like text.
	MaxSniffText = 2 << 20
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
	add(artifactType{"text/markdown; charset=utf-8", Markdown}, "md", "markdown")
	add(artifactType{"text/plain; charset=utf-8", Text},
		"txt", "log", "py", "js", "mjs", "cjs", "ts", "tsx", "jsx", "go", "rs", "java", "c", "h", "cpp", "cc", "hpp",
		"cs", "sh", "bash", "zsh", "yaml", "yml", "toml", "ini", "cfg", "conf", "sql", "css", "scss", "rb", "php",
		"swift", "kt", "lua", "pl", "r", "tex", "rst", "jsonl", "ndjson", "vue", "svelte", "gradle", "mmd", "mermaid")
	add(artifactType{"application/xml", Text}, "xml")
	// A page older than the diff view does not know it and offers the file as a download; the
	// type is still text, so the download is the patch itself.
	add(artifactType{"text/x-diff; charset=utf-8", Diff}, "diff", "patch")
	add(artifactType{"application/json", JSON}, "json")
	add(artifactType{"text/csv; charset=utf-8", CSV}, "csv")
	add(artifactType{"text/tab-separated-values; charset=utf-8", CSV}, "tsv")
	add(artifactType{"image/png", Image}, "png")
	add(artifactType{"image/jpeg", Image}, "jpg", "jpeg")
	add(artifactType{"image/gif", Image}, "gif")
	add(artifactType{"image/webp", Image}, "webp")
	add(artifactType{"image/svg+xml", Image}, "svg")
	add(artifactType{"application/pdf", PDF}, "pdf")
	add(artifactType{"audio/mpeg", Audio}, "mp3")
	add(artifactType{"audio/wav", Audio}, "wav")
	add(artifactType{"audio/ogg", Audio}, "ogg")
	add(artifactType{"audio/mp4", Audio}, "m4a")
	add(artifactType{"video/mp4", Video}, "mp4")
	add(artifactType{"video/webm", Video}, "webm")
	add(artifactType{"text/html; charset=utf-8", HTML}, "html", "htm")
	add(artifactType{"application/zip", None}, "zip")
	add(artifactType{"application/vnd.openxmlformats-officedocument.wordprocessingml.document", None}, "docx")
	add(artifactType{"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", None}, "xlsx")
	add(artifactType{"application/vnd.openxmlformats-officedocument.presentationml.presentation", None}, "pptx")
	return m
}

// artifactByBase names extensionless files that are plain text.
var artifactByBase = map[string]bool{
	"dockerfile": true, "makefile": true, "readme": true, "license": true, "changelog": true, "gemfile": true,
}

// Classify decides the stored content type and the view of a file from
// its (sanitised) name and its first bytes. size is the whole file's size.
func Classify(name string, head []byte, size int64) (contentType, view string) {
	name = path.Base(name)
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	t, known := artifactByExt[ext]
	if !known && ext == "" && artifactByBase[strings.ToLower(name)] {
		t, known = artifactType{"text/plain; charset=utf-8", Text}, true
	}
	sniffed := http.DetectContentType(head)
	if known {
		// A binary view is only believed when the bytes agree: a "png" that is
		// really something else is offered as a download, not shown.
		switch t.view {
		case Image:
			if t.contentType != "image/svg+xml" && !strings.HasPrefix(sniffed, t.contentType) {
				return OctetStream, None
			}
		case PDF:
			if sniffed != "application/pdf" {
				return OctetStream, None
			}
		}
		return t.contentType, t.view
	}
	switch {
	case strings.HasPrefix(sniffed, "image/png"), strings.HasPrefix(sniffed, "image/jpeg"),
		strings.HasPrefix(sniffed, "image/gif"), strings.HasPrefix(sniffed, "image/webp"):
		return strings.SplitN(sniffed, ";", 2)[0], Image
	case sniffed == "application/pdf":
		return sniffed, PDF
	case strings.HasPrefix(sniffed, "text/") && size <= MaxSniffText && utf8.Valid(head):
		// Even HTML that arrives without a telling name is shown as text.
		return "text/plain; charset=utf-8", Text
	}
	return OctetStream, None
}

// ViewForContentType is the view of a stored content type. The table keeps the two in step (a
// test checks Classify against it), which is why the database stores only the type.
func ViewForContentType(stored string) string {
	base := strings.ToLower(strings.TrimSpace(strings.SplitN(stored, ";", 2)[0]))
	switch {
	case base == "text/markdown":
		return Markdown
	case base == "text/plain", base == "application/xml":
		return Text
	case base == "text/x-diff", base == "text/x-patch":
		return Diff
	case base == "application/json":
		return JSON
	case base == "text/csv", base == "text/tab-separated-values":
		return CSV
	case strings.HasPrefix(base, "image/"):
		return Image
	case base == "application/pdf":
		return PDF
	case strings.HasPrefix(base, "audio/"):
		return Audio
	case strings.HasPrefix(base, "video/"):
		return Video
	case base == "text/html":
		return HTML
	}
	return None
}
