package zb

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	stdpath "path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

type zipEntry struct {
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size"`
}

type zipSession struct {
	ID        string
	Path      string
	ReqPath   string
	Password  string
	Encrypted bool
	Entries   []zipEntry
	ByPath    map[string]zipEntry
	CreatedAt time.Time
}

var (
	zipMu       sync.RWMutex
	zipSessions = map[string]*zipSession{}
)

const zipSessionTTL = 30 * time.Minute

func newSessionID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf)
}

func pruneZipSessions() {
	now := time.Now()
	zipMu.Lock()
	defer zipMu.Unlock()
	for id, session := range zipSessions {
		if now.Sub(session.CreatedAt) > zipSessionTTL {
			delete(zipSessions, id)
		}
	}
}

func getZipSession(id string) *zipSession {
	zipMu.RLock()
	defer zipMu.RUnlock()
	session, ok := zipSessions[id]
	if !ok {
		return nil
	}
	if time.Since(session.CreatedAt) > zipSessionTTL {
		return nil
	}
	return session
}

func flattenTree(nodes []model.ObjTree, prefix string, out *[]zipEntry, byPath map[string]zipEntry) {
	for _, node := range nodes {
		name := strings.Trim(strings.TrimPrefix(strings.ReplaceAll(node.GetName(), "\\", "/"), "/"), " ")
		if name == "" {
			flattenTree(node.GetChildren(), prefix, out, byPath)
			continue
		}
		name = strings.TrimSuffix(name, "/")
		// Some archive backends hand back a full relative path instead of a leaf
		// name, so a name that already contains a separator is taken as-is.
		entryPath := name
		if prefix != "" && !strings.Contains(name, "/") {
			entryPath = stdpath.Join(prefix, name)
		}
		if _, exists := byPath[entryPath]; exists {
			continue
		}
		entry := zipEntry{Path: entryPath, Dir: node.IsDir(), Size: node.GetSize()}
		if entry.Dir {
			entry.Size = 0
		}
		*out = append(*out, entry)
		byPath[entryPath] = entry
		if node.IsDir() {
			flattenTree(node.GetChildren(), entryPath, out, byPath)
		}
	}
}

func buildZipSession(c *gin.Context, user *model.User, p string, password string) (*zipSession, error) {
	reqPath, err := user.JoinPath(p)
	if err != nil {
		return nil, err
	}
	if _, err := loadMeta(c, user, reqPath); err != nil {
		return nil, err
	}
	if !user.CanReadArchives() {
		return nil, errors.New("当前用户没有浏览压缩包的权限")
	}
	provider, metaErr := fs.ArchiveMeta(c.Request.Context(), reqPath, model.ArchiveMetaArgs{
		ArchiveArgs: model.ArchiveArgs{Password: password},
	})
	session := &zipSession{
		ID:        newSessionID(),
		Path:      p,
		ReqPath:   reqPath,
		Password:  password,
		CreatedAt: time.Now(),
	}
	if metaErr != nil {
		if errors.Is(metaErr, errs.WrongArchivePassword) {
			session.ByPath = map[string]zipEntry{}
			return session, nil
		}
		return nil, metaErr
	}
	entries := make([]zipEntry, 0, 64)
	byPath := make(map[string]zipEntry, 64)
	flattenTree(provider.GetTree(), "", &entries, byPath)
	session.Entries = entries
	session.ByPath = byPath
	session.Encrypted = provider.IsEncrypted()
	return session, nil
}

type zipOpenReq struct {
	Path     string `json:"path"`
	Password string `json:"password"`
}

func apiZipOpen(c *gin.Context, user *model.User) {
	var req zipOpenReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	p := normalizeVirtual(req.Path)
	session, err := buildZipSession(c, user, p, req.Password)
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	pruneZipSessions()
	zipMu.Lock()
	zipSessions[session.ID] = session
	zipMu.Unlock()
	needsPassword := session.Encrypted && session.Password == ""
	writeJSON(c, http.StatusOK, gin.H{
		"id":            session.ID,
		"path":          p,
		"entries":       session.Entries,
		"needsPassword": needsPassword,
		"encrypted":     session.Encrypted,
	})
}

func apiZipUnlock(c *gin.Context, user *model.User) {
	var req struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	session := getZipSession(req.ID)
	if session == nil {
		writeError(c, http.StatusNotFound, "压缩包会话已失效，请重新打开")
		return
	}
	provider, err := fs.ArchiveMeta(c.Request.Context(), session.ReqPath, model.ArchiveMetaArgs{
		ArchiveArgs: model.ArchiveArgs{Password: req.Password},
	})
	if err != nil {
		if errors.Is(err, errs.WrongArchivePassword) {
			writeError(c, http.StatusUnauthorized, "密码不正确")
			return
		}
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	entries := make([]zipEntry, 0, 64)
	byPath := make(map[string]zipEntry, 64)
	flattenTree(provider.GetTree(), "", &entries, byPath)
	zipMu.Lock()
	prevPassword := session.Password
	prevEntries := session.Entries
	prevByPath := session.ByPath
	prevEncrypted := session.Encrypted
	session.Password = req.Password
	session.Entries = entries
	session.ByPath = byPath
	session.Encrypted = provider.IsEncrypted()
	zipMu.Unlock()
	// Zip listings expose entry names without decrypting them, so the only reliable
	// way to tell a good password from a bad one is to read one entry.
	if probeErr := probeZipPassword(c, session); probeErr != nil {
		zipMu.Lock()
		session.Password = prevPassword
		session.Entries = prevEntries
		session.ByPath = prevByPath
		session.Encrypted = prevEncrypted
		zipMu.Unlock()
		if isWrongPassword(probeErr) {
			writeError(c, http.StatusUnauthorized, "密码不正确")
			return
		}
		writeError(c, http.StatusBadRequest, probeErr.Error())
		return
	}
	writeJSON(c, http.StatusOK, gin.H{"ok": true, "entries": entries})
}

// isWrongPassword recognizes the several shapes a bad archive password can take:
// OpenList wraps some backends in errs.WrongArchivePassword, while the zip
// library reports its own "invalid password" error.
func isWrongPassword(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errs.WrongArchivePassword) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "invalid password") || strings.Contains(msg, "wrong password")
}

func probeZipPassword(c *gin.Context, session *zipSession) error {
	for _, entry := range session.Entries {
		if entry.Dir {
			continue
		}
		reader, _, err := extractZipEntry(c, session, entry.Path)
		if err != nil {
			return err
		}
		_, _ = io.CopyN(io.Discard, reader, 1)
		_ = reader.Close()
		return nil
	}
	return nil
}

func apiZipTree(c *gin.Context, user *model.User) {
	session := getZipSession(c.Query("id"))
	if session == nil {
		writeError(c, http.StatusNotFound, "压缩包会话已失效，请重新打开")
		return
	}
	zipMu.RLock()
	entries := session.Entries
	p := session.Path
	zipMu.RUnlock()
	writeJSON(c, http.StatusOK, gin.H{"id": session.ID, "path": p, "entries": entries})
}

func apiZipClose(c *gin.Context, user *model.User) {
	var req struct {
		ID string `json:"id"`
	}
	_ = c.ShouldBindJSON(&req)
	zipMu.Lock()
	delete(zipSessions, req.ID)
	zipMu.Unlock()
	writeJSON(c, http.StatusOK, gin.H{"ok": true})
}

func handleZipContent(c *gin.Context, user *model.User, rest string) {
	if !requireUser(c, user) {
		return
	}
	segments := splitSegments(rest)
	if len(segments) == 0 {
		c.String(http.StatusBadRequest, "缺少压缩包会话 id")
		return
	}
	id := segments[0]
	entry := strings.Join(segments[1:], "/")
	session := getZipSession(id)
	if session == nil {
		c.String(http.StatusNotFound, "压缩包会话已失效，请重新打开")
		return
	}
	view := c.Query("view")
	if view != "thumbs" {
		view = "list"
	}
	// Directory index files inside the archive take precedence over the listing.
	if view == "list" {
		for _, candidate := range []string{"index.html", "index.htm", "default.html"} {
			probe := candidate
			if entry != "" {
				probe = entry + "/" + candidate
			}
			if record, ok := session.ByPath[probe]; ok && !record.Dir {
				serveZipEntryPage(c, session, probe)
				return
			}
		}
	}
	record, ok := session.ByPath[entry]
	if ok && !record.Dir {
		if isNavigateRequest(c) && c.Query("raw") != "1" {
			serveZipEntryPage(c, session, entry)
			return
		}
		serveZipEntryRaw(c, session, entry)
		return
	}
	if entry == "" || (ok && record.Dir) || hasZipChildren(session, entry) {
		renderZipDirectory(c, session, entry, view)
		return
	}
	c.String(http.StatusNotFound, "包内路径不存在")
}

func hasZipChildren(session *zipSession, dir string) bool {
	prefix := dir + "/"
	if dir == "" {
		prefix = ""
	}
	zipMu.RLock()
	defer zipMu.RUnlock()
	for _, entry := range session.Entries {
		if strings.HasPrefix(entry.Path, prefix) && entry.Path != dir {
			return true
		}
	}
	return false
}

func zipChildren(session *zipSession, dir string) []zipEntry {
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	children := make([]zipEntry, 0, 32)
	seen := map[string]bool{}
	zipMu.RLock()
	for _, entry := range session.Entries {
		if !strings.HasPrefix(entry.Path, prefix) {
			continue
		}
		rest := strings.TrimPrefix(entry.Path, prefix)
		if rest == "" {
			continue
		}
		first := strings.SplitN(rest, "/", 2)[0]
		if seen[first] {
			continue
		}
		seen[first] = true
		childPath := first
		if prefix != "" {
			childPath = prefix + first
		}
		isDir := strings.Contains(rest, "/") || entry.Dir
		child := zipEntry{Path: childPath, Dir: isDir, Size: entry.Size}
		if isDir {
			child.Size = 0
		}
		children = append(children, child)
	}
	zipMu.RUnlock()
	return children
}

func renderZipDirectory(c *gin.Context, session *zipSession, dir string, view string) {
	children := zipChildren(session, dir)
	items := make([]dirItem, 0, len(children)+1)
	if dir != "" {
		parent := ""
		if index := strings.LastIndex(dir, "/"); index > 0 {
			parent = dir[:index]
		}
		items = append(items, dirItem{Name: "..", Dir: true, Href: zipFileURL(session.ID, parent) + "/"})
	}
	for _, child := range children {
		name := baseName(child.Path)
		href := zipFileURL(session.ID, child.Path)
		var thumb string
		if child.Dir {
			href += "/"
		} else if isImage(name) {
			thumb = zipThumbURL(session.ID, child.Path)
		} else if isArchiveName(name) {
			thumb = baseURL() + "/thumb/zipcover/zip/" + session.ID + "/" + strings.TrimPrefix(escapeVirtual(child.Path), "/")
		}
		items = append(items, dirItem{Name: name, Dir: child.Dir, Size: child.Size, Href: href, Thumb: thumb})
	}
	sortDirItems(items)
	title := "zip://" + session.Path + "/"
	if dir != "" {
		title = "zip://" + dir + "/"
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(renderDirectory(title, items, "压缩包内目录 · 相对引用会按包内路径解析", view)))
}

func serveZipEntryPage(c *gin.Context, session *zipSession, entry string) {
	content, err := readZipEntryBytes(c, session, entry, maxPreviewBytes)
	if err != nil {
		if isWrongPassword(err) {
			c.Data(http.StatusUnauthorized, "text/html; charset=utf-8", []byte(renderNotice("需要密码", "这个压缩包已加密，请返回文件浏览器重新输入密码。")))
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(renderNotice("无法预览", err.Error())))
		return
	}
	name := baseName(entry)
	if isMarkdown(name) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(renderMarkdown(string(content), name)))
		return
	}
	if isImage(name) && !strings.EqualFold(extensionOf(name), ".svg") {
		raw := zipFileURL(session.ID, entry) + "?raw=1"
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(renderImageViewer(name, raw)))
		return
	}
	if isTextPreview(name) {
		if strings.EqualFold(extensionOf(name), ".svg") {
			c.Data(http.StatusOK, "image/svg+xml; charset=utf-8", content)
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(renderTextPreview(string(content), name)))
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(injectBase(string(content), zipFileURL(session.ID, entry)+"/")))
}

// extractZipEntry tries the separators archive backends may expect. Zip listings
// on Windows come back with backslash separators, while our virtual paths and
// URLs are always slash-separated.
func extractZipEntry(c *gin.Context, session *zipSession, entry string) (io.ReadCloser, int64, error) {
	zipMu.RLock()
	password := session.Password
	reqPath := session.ReqPath
	zipMu.RUnlock()
	candidates := []string{entry}
	if strings.Contains(entry, "/") {
		candidates = append(candidates, strings.ReplaceAll(entry, "/", "\\"))
	} else if strings.Contains(entry, "\\") {
		candidates = append(candidates, strings.ReplaceAll(entry, "\\", "/"))
	}
	var lastErr error
	for _, candidate := range candidates {
		reader, size, err := fs.ArchiveInternalExtract(c.Request.Context(), reqPath, model.ArchiveInnerArgs{
			ArchiveArgs: model.ArchiveArgs{Password: password},
			InnerPath:   candidate,
		})
		if err == nil {
			return reader, size, nil
		}
		lastErr = err
	}
	return nil, 0, lastErr
}

func readZipEntryBytes(c *gin.Context, session *zipSession, entry string, limit int64) ([]byte, error) {
	reader, _, err := extractZipEntry(c, session, entry)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(io.LimitReader(reader, limit))
}

func serveZipEntryRaw(c *gin.Context, session *zipSession, entry string) {
	reader, size, err := extractZipEntry(c, session, entry)
	if err != nil {
		if isWrongPassword(err) {
			c.Data(http.StatusUnauthorized, "text/html; charset=utf-8", []byte(renderNotice("需要密码", "这个压缩包已加密，请返回文件浏览器重新输入密码。")))
			return
		}
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	defer reader.Close()
	name := baseName(entry)
	applyDisposition(c, name, true, mimeOf(name))
	if size > 0 {
		c.Header("Content-Length", strconv.FormatInt(size, 10))
	}
	c.Status(http.StatusOK)
	if c.Request.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(c.Writer, reader)
}

func openZipCover(c *gin.Context, user *model.User, p string, password string) (*zipSession, []byte, error) {
	session, err := buildZipSession(c, user, p, password)
	if err != nil {
		return nil, nil, err
	}
	if len(session.Entries) != 1 {
		return session, nil, nil
	}
	entry := session.Entries[0]
	if entry.Dir || !isImage(baseName(entry.Path)) {
		return session, nil, nil
	}
	content, err := readZipEntryBytes(c, session, entry.Path, 32<<20)
	if err != nil {
		return session, nil, err
	}
	return session, content, nil
}

func archivePlaceholder(c *gin.Context) {
	c.Header("Content-Type", "image/svg+xml")
	c.Header("Cache-Control", "public, max-age=86400")
	c.Data(http.StatusOK, "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="160" height="120" viewBox="0 0 160 120"><rect width="160" height="120" fill="#f3f5f8"/><path d="M52 28h32l24 24v40a4 4 0 0 1-4 4H52a4 4 0 0 1-4-4V32a4 4 0 0 1 4-4z" fill="#dfe5ee" stroke="#b9c3d1"/><path d="M84 28v24h24" fill="#eef2f7" stroke="#b9c3d1"/><rect x="77" y="60" width="10" height="16" rx="3" fill="#8fa3bd"/><rect x="77" y="78" width="10" height="6" rx="3" fill="#8fa3bd"/><text x="80" y="104" font-size="12" text-anchor="middle" fill="#7f8ca0">压缩包</text></svg>`))
}
