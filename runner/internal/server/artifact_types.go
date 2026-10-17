package server

import (
	"path"
	"strings"
	"unicode/utf8"

	"github.com/blerglab/blerg-ai/runner/internal/fileview"
)

// Shared classification keeps workspace and published artifact views consistent.
const (
	viewMarkdown = fileview.Markdown
	viewText     = fileview.Text
	viewJSON     = fileview.JSON
	viewCSV      = fileview.CSV
	viewImage    = fileview.Image
	viewPDF      = fileview.PDF
	viewAudio    = fileview.Audio
	viewVideo    = fileview.Video
	viewHTML     = fileview.HTML
	viewNone     = fileview.None
	octetStream  = fileview.OctetStream
	maxSniffText = fileview.MaxSniffText
)

func classifyArtifact(name string, head []byte, size int64) (string, string) {
	return fileview.Classify(name, head, size)
}
func viewForContentType(stored string) string { return fileview.ViewForContentType(stored) }

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
