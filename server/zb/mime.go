package zb

import "strings"

var typeMap = map[string]string{
	".html": "text/html", ".htm": "text/html", ".css": "text/css", ".js": "text/javascript",
	".mjs": "text/javascript", ".cjs": "text/javascript", ".json": "application/json",
	".map": "application/json", ".txt": "text/plain", ".md": "text/markdown",
	".markdown": "text/markdown", ".xml": "text/xml", ".csv": "text/csv",
	".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".avif": "image/avif", ".bmp": "image/bmp",
	".ico": "image/x-icon", ".woff": "font/woff", ".woff2": "font/woff2", ".ttf": "font/ttf",
	".otf": "font/otf", ".eot": "application/vnd.ms-fontobject", ".pdf": "application/pdf",
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg", ".m4a": "audio/mp4",
	".mp4": "video/mp4", ".webm": "video/webm", ".wasm": "application/wasm",
	".yaml": "text/plain", ".yml": "text/plain",
}

var textExtensions = map[string]bool{
	".html": true, ".htm": true, ".css": true, ".js": true, ".mjs": true, ".cjs": true,
	".json": true, ".map": true, ".txt": true, ".md": true, ".markdown": true, ".xml": true,
	".csv": true, ".svg": true, ".yaml": true, ".yml": true, ".ts": true, ".tsx": true,
	".jsx": true, ".vue": true, ".py": true, ".go": true, ".rs": true, ".java": true,
	".c": true, ".h": true, ".cpp": true, ".sh": true, ".bat": true, ".ps1": true,
	".log": true, ".ini": true, ".conf": true, ".sql": true, ".gitignore": true,
}

var previewExtensions = map[string]bool{
	".html": true, ".htm": true, ".css": true, ".js": true, ".mjs": true, ".json": true,
	".txt": true, ".md": true, ".markdown": true, ".xml": true, ".csv": true, ".svg": true,
	".yaml": true, ".yml": true, ".ts": true, ".tsx": true, ".jsx": true, ".vue": true,
	".py": true, ".go": true, ".rs": true, ".java": true, ".c": true, ".h": true, ".cpp": true,
	".sh": true, ".bat": true, ".ps1": true, ".log": true, ".ini": true, ".conf": true,
	".sql": true, ".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".avif": true, ".bmp": true, ".ico": true, ".pdf": true, ".mp3": true, ".wav": true,
	".ogg": true, ".m4a": true, ".mp4": true, ".webm": true, ".woff": true, ".woff2": true,
	".ttf": true,
}

var imageExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".avif": true, ".bmp": true, ".ico": true, ".svg": true,
}

var archiveExtensions = map[string]bool{
	".zip": true, ".7z": true, ".rar": true, ".tar": true, ".tgz": true, ".gz": true,
	".bz2": true, ".xz": true, ".iso": true, ".cab": true, ".wim": true, ".lzh": true,
	".arj": true, ".z": true, ".rpm": true, ".deb": true, ".cpio": true,
}

func extensionOf(name string) string {
	base := baseName(name)
	index := strings.LastIndex(base, ".")
	if index <= 0 {
		return ""
	}
	return strings.ToLower(base[index:])
}

func mimeOf(name string) string {
	ext := extensionOf(name)
	value, ok := typeMap[ext]
	if !ok {
		if textExtensions[ext] {
			value = "text/plain"
		} else {
			value = "application/octet-stream"
		}
	}
	if strings.HasPrefix(value, "text/") || value == "application/json" || value == "image/svg+xml" {
		return value + "; charset=utf-8"
	}
	return value
}

func isImage(name string) bool       { return imageExtensions[extensionOf(name)] }
func isTextPreview(name string) bool { return textExtensions[extensionOf(name)] }

func isMarkdown(name string) bool {
	ext := extensionOf(name)
	return ext == ".md" || ext == ".markdown"
}

func isPreviewable(name string) bool { return previewExtensions[extensionOf(name)] }

func isArchiveName(name string) bool {
	lower := strings.ToLower(name)
	if archiveExtensions[extensionOf(lower)] {
		return true
	}
	for _, suffix := range []string{".tar.gz", ".tar.bz2", ".tar.xz", ".tar.zst"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}
