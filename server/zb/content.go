package zb

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	stdpath "path"
	"strconv"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

const maxPreviewBytes = 16 << 20

func isNavigateRequest(c *gin.Context) bool {
	mode := strings.ToLower(c.GetHeader("Sec-Fetch-Mode"))
	dest := strings.ToLower(c.GetHeader("Sec-Fetch-Dest"))
	if mode == "navigate" || dest == "document" || dest == "iframe" {
		return true
	}
	switch dest {
	case "style", "script", "image", "font", "audio", "video", "manifest":
		return false
	}
	accept := c.GetHeader("Accept")
	return strings.Contains(accept, "text/html") && !strings.Contains(accept, "*/*")
}

func loadMeta(c *gin.Context, user *model.User, reqPath string) (*model.Meta, error) {
	meta, err := op.GetNearestMeta(reqPath)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		return nil, err
	}
	common.GinAppendValues(c, conf.MetaKey, meta)
	if !common.CanAccess(user, meta, reqPath, "") {
		return nil, errs.PermissionDenied
	}
	return meta, nil
}

func handleContent(c *gin.Context, user *model.User, rawPath string) {
	if !requireUser(c, user) {
		return
	}
	p := normalizeVirtual(rawPath)
	reqPath, err := user.JoinPath(p)
	if err != nil {
		c.String(http.StatusForbidden, "路径不可访问")
		return
	}
	if _, err := loadMeta(c, user, reqPath); err != nil {
		c.String(http.StatusForbidden, "无权访问该目录")
		return
	}
	obj, err := fs.Get(c.Request.Context(), reqPath, &fs.GetArgs{})
	if err != nil {
		c.String(http.StatusNotFound, "not found")
		return
	}
	view := c.Query("view")
	if view != "thumbs" {
		view = "list"
	}
	if obj.IsDir() {
		if view == "list" {
			if indexPath := findIndex(c, reqPath); indexPath != "" {
				serveIndexFile(c, indexPath, p)
				return
			}
		}
		renderDirectoryPage(c, p, reqPath, view)
		return
	}
	if isNavigateRequest(c) && c.Query("raw") != "1" {
		servePreview(c, reqPath, obj, p)
		return
	}
	serveStream(c, reqPath, obj, p, true)
}

func findIndex(c *gin.Context, reqPath string) string {
	for _, name := range []string{"index.html", "index.htm"} {
		candidate := stdpath.Join(reqPath, name)
		if obj, err := fs.Get(c.Request.Context(), candidate, &fs.GetArgs{NoLog: true}); err == nil && !obj.IsDir() {
			return candidate
		}
	}
	return ""
}

func renderDirectoryPage(c *gin.Context, display string, reqPath string, view string) {
	objs, err := fs.List(c.Request.Context(), reqPath, &fs.ListArgs{NoLog: true})
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]dirItem, 0, len(objs))
	for _, obj := range objs {
		name := obj.GetName()
		childDisplay := joinVirtual(display, name)
		href := fsURL(childDisplay)
		var thumb string
		if obj.IsDir() {
			href += "/"
		} else if isImage(name) {
			thumb = thumbURL(childDisplay)
		} else if isArchiveName(name) {
			thumb = baseURL() + "/thumb/zipcover/fs" + escapeVirtual(childDisplay)
		}
		items = append(items, dirItem{
			Name:  name,
			Href:  href,
			Thumb: thumb,
			Dir:   obj.IsDir(),
			Size:  obj.GetSize(),
		})
	}
	sortDirItems(items)
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(renderDirectory(display, items, "OpenList 目录 · 只读浏览，点击文件名可直接预览", view)))
}

func serveIndexFile(c *gin.Context, indexPath string, display string) {
	content, err := readBytes(c, indexPath, maxPreviewBytes)
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(injectBase(string(content), fsURL(display)+"/")))
}

// injectBase makes relative references inside an index.html resolve against the directory URL.
func injectBase(page string, href string) string {
	base := `<base href="` + escapeHTML(href) + `">`
	lower := strings.ToLower(page)
	if index := strings.Index(lower, "<head"); index >= 0 {
		if end := strings.Index(lower[index:], ">"); end >= 0 {
			insert := index + end + 1
			return page[:insert] + base + page[insert:]
		}
	}
	return base + page
}

func servePreview(c *gin.Context, reqPath string, obj model.Obj, display string) {
	name := obj.GetName()
	if isImage(name) && !strings.EqualFold(extensionOf(name), ".svg") {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(renderImageViewer(name, fsURL(display)+"?raw=1")))
		return
	}
	if !isTextPreview(name) {
		serveStream(c, reqPath, obj, display, true)
		return
	}
	content, err := readBytes(c, reqPath, maxPreviewBytes)
	if err != nil {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(renderNotice("无法预览", err.Error())))
		return
	}
	if isMarkdown(name) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(renderMarkdown(string(content), name)))
		return
	}
	if strings.EqualFold(extensionOf(name), ".svg") {
		c.Data(http.StatusOK, "image/svg+xml; charset=utf-8", content)
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(renderTextPreview(string(content), name)))
}

func readBytes(c *gin.Context, reqPath string, limit int64) ([]byte, error) {
	link, _, err := fs.Link(c.Request.Context(), reqPath, model.LinkArgs{
		IP:     c.ClientIP(),
		Header: c.Request.Header,
	})
	if err != nil {
		return nil, err
	}
	defer link.Close()
	return readLinkBytes(c, link, limit)
}

func serveStream(c *gin.Context, reqPath string, obj model.Obj, display string, inline bool) {
	link, _, err := fs.Link(c.Request.Context(), reqPath, model.LinkArgs{
		IP:     c.ClientIP(),
		Header: c.Request.Header,
	})
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	defer link.Close()
	contentType := mimeOf(obj.GetName())
	applyDisposition(c, obj.GetName(), inline, contentType)
	if link.URL != "" {
		proxyExternal(c, link.URL, link.Header, contentType)
		return
	}
	if link.RangeReader != nil {
		streamRangeReader(c, link.RangeReader, obj.GetSize(), contentType)
		return
	}
	c.String(http.StatusInternalServerError, "该驱动未提供可用的下载链接")
}

func serveDownload(c *gin.Context, reqPath string, obj model.Obj, name string) {
	link, _, err := fs.Link(c.Request.Context(), reqPath, model.LinkArgs{
		IP:     c.ClientIP(),
		Header: c.Request.Header,
	})
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	defer link.Close()
	contentType := mimeOf(name)
	applyDisposition(c, name, false, contentType)
	if link.URL != "" {
		proxyExternal(c, link.URL, link.Header, contentType)
		return
	}
	if link.RangeReader != nil {
		streamRangeReader(c, link.RangeReader, obj.GetSize(), contentType)
		return
	}
	c.String(http.StatusInternalServerError, "该驱动未提供可用的下载链接")
}

func applyDisposition(c *gin.Context, name string, inline bool, contentType string) {
	c.Header("Content-Type", contentType)
	disposition := "attachment"
	if inline {
		disposition = "inline"
	}
	c.Header("Content-Disposition", fmt.Sprintf("%s; filename=%q; filename*=UTF-8''%s", disposition, asciiFallback(name), url.PathEscape(name)))
}

func asciiFallback(name string) string {
	var builder strings.Builder
	for _, r := range name {
		if r >= 32 && r < 128 && r != '"' && r != '\\' {
			builder.WriteRune(r)
		} else {
			builder.WriteRune('_')
		}
	}
	if builder.Len() == 0 {
		return "download"
	}
	return builder.String()
}

func proxyExternal(c *gin.Context, linkURL string, extra http.Header, contentType string) {
	request, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, linkURL, nil)
	if err != nil {
		c.String(http.StatusBadGateway, err.Error())
		return
	}
	for key, values := range extra {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	for _, key := range []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"} {
		if value := c.GetHeader(key); value != "" {
			request.Header.Set(key, value)
		}
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		c.String(http.StatusBadGateway, err.Error())
		return
	}
	defer response.Body.Close()
	for _, key := range []string{"Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
		if value := response.Header.Get(key); value != "" {
			c.Header(key, value)
		}
	}
	if contentType != "" {
		c.Header("Content-Type", contentType)
	} else if value := response.Header.Get("Content-Type"); value != "" {
		c.Header("Content-Type", value)
	}
	c.Status(response.StatusCode)
	if c.Request.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(c.Writer, response.Body)
}

func streamRangeReader(c *gin.Context, reader model.RangeReaderIF, size int64, contentType string) {
	c.Header("Accept-Ranges", "bytes")
	c.Header("Content-Type", contentType)
	rangeHeader := c.GetHeader("Range")
	if rangeHeader == "" || size <= 0 {
		if size > 0 {
			c.Header("Content-Length", strconv.FormatInt(size, 10))
		}
		c.Status(http.StatusOK)
		if c.Request.Method == http.MethodHead {
			return
		}
		stream, err := reader.RangeRead(c.Request.Context(), http_range.Range{Start: 0, Length: -1})
		if err != nil {
			c.String(http.StatusInternalServerError, err.Error())
			return
		}
		defer stream.Close()
		_, _ = io.Copy(c.Writer, stream)
		return
	}
	ranges, err := http_range.ParseRange(rangeHeader, size)
	if err != nil || len(ranges) != 1 {
		c.Header("Content-Range", fmt.Sprintf("bytes */%d", size))
		c.Status(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	part := ranges[0]
	stream, err := reader.RangeRead(c.Request.Context(), part)
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	defer stream.Close()
	c.Header("Content-Range", part.ContentRange(size))
	c.Header("Content-Length", strconv.FormatInt(part.Length, 10))
	c.Status(http.StatusPartialContent)
	if c.Request.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(c.Writer, stream)
}

func readLinkBytes(c *gin.Context, link *model.Link, limit int64) ([]byte, error) {
	if link.URL != "" {
		request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, link.URL, nil)
		if err != nil {
			return nil, err
		}
		for key, values := range link.Header {
			for _, value := range values {
				request.Header.Add(key, value)
			}
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode >= 400 {
			return nil, fmt.Errorf("上游返回 %d", response.StatusCode)
		}
		return io.ReadAll(io.LimitReader(response.Body, limit))
	}
	if link.RangeReader != nil {
		stream, err := link.RangeReader.RangeRead(c.Request.Context(), http_range.Range{Start: 0, Length: -1})
		if err != nil {
			return nil, err
		}
		defer stream.Close()
		return io.ReadAll(io.LimitReader(stream, limit))
	}
	return nil, fmt.Errorf("驱动没有可读取的链接")
}

func intParam(c *gin.Context, key string, fallback int) int {
	value := c.Query(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
