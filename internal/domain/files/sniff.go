package files

import (
	"archive/zip"
	"bytes"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Detect reports the media type of content from its magic bytes, not from a
// name or a Content-Type header. Those two are caller-controlled and are the
// first thing a malicious upload forges.
func Detect(content []byte) string {
	if len(content) == 0 {
		return ""
	}

	if kind := sniffExecutable(content); kind != "" {
		return kind
	}

	switch {
	case bytes.HasPrefix(content, []byte("%PDF")):
		return "application/pdf"
	case bytes.HasPrefix(content, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case len(content) >= 3 && bytes.Equal(content[:3], []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg"
	case bytes.HasPrefix(content, []byte("GIF87a")) || bytes.HasPrefix(content, []byte("GIF89a")):
		return "image/gif"
	case isWebP(content):
		return "image/webp"
	case bytes.HasPrefix(content, []byte{0x1f, 0x8b}):
		return "application/gzip"
	case isZip(content):
		return sniffZip(content)
	}

	trimmed := bytes.TrimLeftFunc(content, unicode.IsSpace)
	lower := bytes.ToLower(trimmed)

	switch {
	case bytes.HasPrefix(lower, []byte("<!doctype html")) || bytes.HasPrefix(lower, []byte("<html")):
		return "text/html"
	case bytes.HasPrefix(trimmed, []byte("<?xml")):
		return "application/xml"
	}

	if utf8.Valid(content) && isMostlyText(content) {
		s := strings.TrimSpace(string(trimmed))
		if (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) ||
			(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) {
			return "application/json"
		}

		return "text/plain"
	}

	// Last resort: the stdlib sniffer, which is what a browser would do with
	// the same prefix. It is weaker than the cases above and must not override
	// them — an MZ file is an executable even if DetectContentType calls it
	// application/octet-stream.
	detected := http.DetectContentType(content)
	if i := strings.IndexByte(detected, ';'); i >= 0 {
		detected = detected[:i]
	}

	return strings.ToLower(strings.TrimSpace(detected))
}

func sniffExecutable(content []byte) string {
	switch {
	case bytes.HasPrefix(content, []byte("MZ")):
		return "application/x-msdownload"
	case bytes.HasPrefix(content, []byte("\x7fELF")):
		return "application/x-elf"
	case isMachO(content):
		return "application/x-mach-binary"
	case bytes.HasPrefix(content, []byte("\x00asm")):
		return "application/wasm"
	case bytes.HasPrefix(content, []byte("#!")):
		return "application/x-sh"
	default:
		return ""
	}
}

func isMachO(content []byte) bool {
	if len(content) < 4 {
		return false
	}

	switch string(content[:4]) {
	case "\xfe\xed\xfa\xce", "\xce\xfa\xed\xfe",
		"\xfe\xed\xfa\xcf", "\xcf\xfa\xed\xfe",
		"\xca\xfe\xba\xbe", "\xbe\xba\xfe\xca":
		return true
	default:
		return false
	}
}

func isWebP(content []byte) bool {
	return len(content) >= 12 &&
		bytes.HasPrefix(content, []byte("RIFF")) &&
		bytes.Equal(content[8:12], []byte("WEBP"))
}

func isZip(content []byte) bool {
	return bytes.HasPrefix(content, []byte("PK\x03\x04")) ||
		bytes.HasPrefix(content, []byte("PK\x05\x06")) ||
		bytes.HasPrefix(content, []byte("PK\x07\x08"))
}

// sniffZip looks inside a ZIP for the well-known OOXML parts. A ZIP of
// malware declared as a spreadsheet is the case this exists for; listing
// names does not decompress the entries, so a zip bomb cannot inflate here.
func sniffZip(content []byte) string {
	r, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return "application/zip"
	}

	var word, excel, ppt bool

	for _, f := range r.File {
		name := strings.ToLower(f.Name)
		switch {
		case strings.HasPrefix(name, "word/"):
			word = true
		case strings.HasPrefix(name, "xl/"):
			excel = true
		case strings.HasPrefix(name, "ppt/"):
			ppt = true
		}
	}

	switch {
	case word && !excel && !ppt:
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case excel && !word && !ppt:
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ppt && !word && !excel:
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	default:
		return "application/zip"
	}
}

func isMostlyText(content []byte) bool {
	if len(content) == 0 {
		return false
	}

	non := 0
	for _, b := range content {
		if b == '\n' || b == '\r' || b == '\t' {
			continue
		}

		if b < 0x20 || b == 0x7f {
			non++
		}
	}

	return non*10 <= len(content)
}

// NormalizeMediaType strips a MIME parameter list and lowercases the type.
func NormalizeMediaType(raw string) string {
	raw = strings.TrimSpace(raw)
	if i := strings.IndexByte(raw, ';'); i >= 0 {
		raw = raw[:i]
	}

	return strings.ToLower(strings.TrimSpace(raw))
}

var mediaAliases = map[string]string{
	"image/jpg":                    "image/jpeg",
	"text/xml":                     "application/xml",
	"application/x-zip-compressed": "application/zip",
	"application/x-zip":            "application/zip",
	"application/javascript":       "text/javascript",
	"application/x-javascript":     "text/javascript",
	"application/vnd.ms-excel":     "application/vnd.ms-excel",
	"text/csv":                     "text/csv",
	"application/csv":              "text/csv",
}

func CanonicalMediaType(raw string) string {
	n := NormalizeMediaType(raw)
	if alias, ok := mediaAliases[n]; ok {
		return alias
	}

	return n
}

var extensionTypes = map[string]string{
	".pdf":  "application/pdf",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".txt":  "text/plain",
	".csv":  "text/csv",
	".json": "application/json",
	".xml":  "application/xml",
	".zip":  "application/zip",
	".gz":   "application/gzip",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
}

var executableExtensions = map[string]bool{
	".exe": true, ".dll": true, ".bat": true, ".cmd": true, ".com": true,
	".scr": true, ".msi": true, ".js": true, ".jse": true, ".vbs": true,
	".vbe": true, ".ps1": true, ".wsf": true, ".sh": true, ".bin": true,
	".elf": true, ".so": true, ".dylib": true, ".wasm": true,
}

func TypeForExtension(ext string) (string, bool) {
	t, ok := extensionTypes[strings.ToLower(ext)]

	return t, ok
}

func ExecutableExtension(ext string) bool {
	return executableExtensions[strings.ToLower(ext)]
}

func typesCompatible(a, b string) bool {
	a, b = CanonicalMediaType(a), CanonicalMediaType(b)
	if a == "" || b == "" {
		return false
	}

	if a == b {
		return true
	}

	// A CSV is valid UTF-8 text; sniffing it as text/plain is expected.
	if (a == "text/csv" && b == "text/plain") || (a == "text/plain" && b == "text/csv") {
		return true
	}

	return false
}
