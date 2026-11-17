package zb

import (
	"net/url"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
)

// baseURL is the mount point of the zip-browser UI, honouring OpenList's own base path.
func baseURL() string {
	prefix := strings.TrimSuffix(conf.URL.Path, "/")
	if prefix == "" || prefix == "/" {
		return "/zb"
	}
	return prefix + "/zb"
}

func splitSegments(p string) []string {
	parts := strings.Split(strings.ReplaceAll(p, "\\", "/"), "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		// 前端缓存/收藏里可能还带着 Windows 盘符写法（C:\codes、C:/codes），
		// OpenList 的虚拟根实际是 /C/…；这里把盘符段 C: 归一成 C，避免出现 /C:/codes。
		if len(out) == 0 && len(part) == 2 && part[1] == ':' && isASCIILetter(part[0]) {
			part = strings.ToUpper(part[:1])
		}
		switch part {
		case "", ".":
			continue
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
			continue
		}
		out = append(out, part)
	}
	return out
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func normalizeVirtual(p string) string {
	segments := splitSegments(p)
	if len(segments) == 0 {
		return "/"
	}
	return "/" + strings.Join(segments, "/")
}

func parentVirtual(p string) string {
	segments := splitSegments(p)
	if len(segments) <= 1 {
		return "/"
	}
	return "/" + strings.Join(segments[:len(segments)-1], "/")
}

func baseName(p string) string {
	segments := splitSegments(p)
	if len(segments) == 0 {
		return ""
	}
	return segments[len(segments)-1]
}

func joinVirtual(parent string, name string) string {
	parent = normalizeVirtual(parent)
	if parent == "/" {
		return "/" + name
	}
	return parent + "/" + name
}

func depthOf(p string) int { return len(splitSegments(p)) }

// escapeVirtual percent-encodes each segment of a virtual path for use in URLs.
func escapeVirtual(p string) string {
	segments := splitSegments(p)
	if len(segments) == 0 {
		return ""
	}
	escaped := make([]string, 0, len(segments))
	for _, segment := range segments {
		escaped = append(escaped, url.PathEscape(segment))
	}
	return "/" + strings.Join(escaped, "/")
}

func fsURL(p string) string       { return baseURL() + "/fs" + escapeVirtual(p) }
func thumbURL(p string) string    { return baseURL() + "/thumb/fs" + escapeVirtual(p) }
func downloadURL(p string) string { return baseURL() + "/api/download?path=" + url.QueryEscape(p) }

func zipFileURL(id string, entry string) string {
	return baseURL() + "/zip/" + url.PathEscape(id) + "/" + strings.TrimPrefix(escapeVirtual(entry), "/")
}

func zipThumbURL(id string, entry string) string {
	return baseURL() + "/thumb/zip/" + url.PathEscape(id) + "/" + strings.TrimPrefix(escapeVirtual(entry), "/")
}
