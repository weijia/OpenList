package zb

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"image"
	"image/jpeg"
	"net/http"
	"strconv"
	"strings"
	"sync"

	_ "image/gif"
	_ "image/png"

	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/gin-gonic/gin"
	"golang.org/x/image/draw"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

const (
	thumbSize       = 160
	maxSourceBytes  = 32 << 20
	thumbCacheLimit = 300
)

const placeholderSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="160" height="160" viewBox="0 0 160 160"><rect width="160" height="160" fill="#f1f3f6"/><rect x="38" y="48" width="84" height="64" rx="7" fill="#dfe4ea"/><circle cx="64" cy="70" r="9" fill="#b9c2cd"/><path d="M42 108l27-27 18 18 13-11 22 20z" fill="#b9c2cd"/></svg>`

const zipIconSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="160" height="160" viewBox="0 0 160 160"><rect width="160" height="160" fill="#f1f3f6"/><rect x="48" y="32" width="64" height="96" rx="8" fill="#c8d2f5"/><path d="M88 32 l0 16 16 -16 z" fill="#aab8f0"/><rect x="60" y="74" width="40" height="6" rx="3" fill="#7c8cf0"/><rect x="60" y="88" width="40" height="6" rx="3" fill="#7c8cf0"/><rect x="60" y="102" width="40" height="6" rx="3" fill="#7c8cf0"/></svg>`

type thumbResult struct {
	data        []byte
	contentType string
}

var (
	thumbMu    sync.RWMutex
	thumbCache = map[string]thumbResult{}
)

func thumbKey(parts ...string) string {
	sum := sha1.Sum([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

func cacheGet(key string) (thumbResult, bool) {
	thumbMu.RLock()
	defer thumbMu.RUnlock()
	value, ok := thumbCache[key]
	return value, ok
}

func cachePut(key string, value thumbResult) {
	thumbMu.Lock()
	defer thumbMu.Unlock()
	if len(thumbCache) >= thumbCacheLimit {
		for k := range thumbCache {
			delete(thumbCache, k)
			break
		}
	}
	thumbCache[key] = value
}

func writeThumb(c *gin.Context, result thumbResult) {
	c.Header("Cache-Control", "public, max-age=300")
	c.Data(http.StatusOK, result.contentType, result.data)
}

func writeIcon(c *gin.Context, svg string) {
	c.Header("Cache-Control", "public, max-age=86400")
	c.Data(http.StatusOK, "image/svg+xml", []byte(svg))
}

func makeThumb(source []byte) (thumbResult, bool) {
	if len(source) == 0 || len(source) > maxSourceBytes {
		return thumbResult{}, false
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(source))
	if err != nil {
		return thumbResult{}, false
	}
	if cfg.Width*cfg.Height > 40_000_000 {
		return thumbResult{}, false
	}
	img, _, err := image.Decode(bytes.NewReader(source))
	if err != nil {
		return thumbResult{}, false
	}
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 {
		return thumbResult{}, false
	}
	// Centre-crop a square region, then scale it to 160x160.
	side := width
	if height < side {
		side = height
	}
	srcX := bounds.Min.X + (width-side)/2
	srcY := bounds.Min.Y + (height-side)/2
	srcRect := image.Rect(srcX, srcY, srcX+side, srcY+side)

	dst := image.NewRGBA(image.Rect(0, 0, thumbSize, thumbSize))
	for index := 0; index < len(dst.Pix); index += 4 {
		dst.Pix[index] = 255
		dst.Pix[index+1] = 255
		dst.Pix[index+2] = 255
		dst.Pix[index+3] = 255
	}
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, srcRect, draw.Over, nil)

	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, dst, &jpeg.Options{Quality: 76}); err != nil {
		return thumbResult{}, false
	}
	return thumbResult{data: buffer.Bytes(), contentType: "image/jpeg"}, true
}

func handleThumb(c *gin.Context, user *model.User, rest string) {
	if !requireUser(c, user) {
		return
	}
	switch {
	case strings.HasPrefix(rest, "fs/"):
		handleFsThumb(c, user, "/"+strings.TrimPrefix(rest, "fs/"))
	case strings.HasPrefix(rest, "zipcover/fs/"):
		handleZipCoverThumb(c, user, "/"+strings.TrimPrefix(rest, "zipcover/fs/"))
	case strings.HasPrefix(rest, "zipcover/zip/"):
		writeIcon(c, zipIconSVG)
	case strings.HasPrefix(rest, "zip/"):
		handleZipThumb(c, user, strings.TrimPrefix(rest, "zip/"))
	default:
		c.Status(http.StatusNotFound)
	}
}

func handleFsThumb(c *gin.Context, user *model.User, rawPath string) {
	p := normalizeVirtual(rawPath)
	reqPath, err := user.JoinPath(p)
	if err != nil {
		writeIcon(c, placeholderSVG)
		return
	}
	obj, err := fs.Get(c.Request.Context(), reqPath, &fs.GetArgs{NoLog: true})
	if err != nil || obj.IsDir() || !isImage(obj.GetName()) {
		writeIcon(c, placeholderSVG)
		return
	}
	key := thumbKey("fs", p, obj.GetID(), obj.GetName(), strconv.FormatInt(obj.GetSize(), 10), strconv.FormatInt(obj.ModTime().UnixMilli(), 10))
	if cached, ok := cacheGet(key); ok {
		writeThumb(c, cached)
		return
	}
	source, err := readBytes(c, reqPath, maxSourceBytes)
	if err != nil {
		writeIcon(c, placeholderSVG)
		return
	}
	result, ok := makeThumb(source)
	if !ok {
		writeIcon(c, placeholderSVG)
		return
	}
	cachePut(key, result)
	writeThumb(c, result)
}

func handleZipThumb(c *gin.Context, user *model.User, rest string) {
	segments := splitSegments(rest)
	if len(segments) < 2 {
		writeIcon(c, placeholderSVG)
		return
	}
	session := getZipSession(segments[0])
	if session == nil {
		writeIcon(c, placeholderSVG)
		return
	}
	entry := strings.Join(segments[1:], "/")
	key := thumbKey("zip", session.ID, entry)
	if cached, ok := cacheGet(key); ok {
		writeThumb(c, cached)
		return
	}
	source, err := readZipEntryBytes(c, session, entry, maxSourceBytes)
	if err != nil {
		writeIcon(c, placeholderSVG)
		return
	}
	result, ok := makeThumb(source)
	if !ok {
		writeIcon(c, placeholderSVG)
		return
	}
	cachePut(key, result)
	writeThumb(c, result)
}

func handleZipCoverThumb(c *gin.Context, user *model.User, rawPath string) {
	p := normalizeVirtual(rawPath)
	defpw := c.Query("defpw")
	key := thumbKey("zipcover", p, defpw)
	if cached, ok := cacheGet(key); ok {
		writeThumb(c, cached)
		return
	}
	_, source, err := openZipCover(c, user, p, defpw)
	if err != nil || len(source) == 0 {
		writeIcon(c, zipIconSVG)
		return
	}
	result, ok := makeThumb(source)
	if !ok {
		writeIcon(c, zipIconSVG)
		return
	}
	cachePut(key, result)
	writeThumb(c, result)
}
