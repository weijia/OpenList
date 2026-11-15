package zb

import (
	"net/http"
	"os"
	stdpath "path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/pkg/errors"
)

// zbSessionTTL keeps the browser signed in for a month, independent of the
// short-lived admin token. Logout clears the cookie, a password change bumps
// PwdTS and invalidates the token.
const zbSessionTTL = 30 * 24 * time.Hour

func generateSessionToken(user *model.User) (string, error) {
	claims := common.UserClaims{
		Username: user.Username,
		PwdTS:    user.PwdTS,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(zbSessionTTL)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(common.SecretKey)
}

type listItem struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Dir       bool   `json:"dir"`
	Size      int64  `json:"size"`
	Mtime     int64  `json:"mtime"`
	Archive   bool   `json:"archive"`
	Zip       bool   `json:"zip"`
	Encrypted any    `json:"encrypted"`
}

type driveItem struct {
	Label   string `json:"label"`
	Path    string `json:"path"`
	Letter  string `json:"letter"`
	Kind    string `json:"kind"`
	Total   int64  `json:"total"`
	Free    int64  `json:"free"`
	Virtual bool   `json:"virtual"`
}

func handleAPI(c *gin.Context, user *model.User, endpoint string) {
	switch {
	case endpoint == "session" || endpoint == "me":
		handleSession(c, user)
		return
	case endpoint == "login":
		handleLogin(c)
		return
	case endpoint == "logout":
		handleLogout(c)
		return
	}
	if !requireUser(c, user) {
		return
	}
	switch {
	case endpoint == "drives":
		apiDrives(c, user)
	case endpoint == "list":
		apiList(c, user)
	case endpoint == "download":
		apiDownload(c, user)
	case endpoint == "hostinfo":
		apiHostInfo(c, user)
	case endpoint == "version":
		apiVersion(c, user)
	case endpoint == "trash":
		apiTrash(c, user)
	case endpoint == "zip/open":
		apiZipOpen(c, user)
	case endpoint == "zip/unlock":
		apiZipUnlock(c, user)
	case endpoint == "zip/tree":
		apiZipTree(c, user)
	case endpoint == "zip/close":
		apiZipClose(c, user)
	case endpoint == "mft/tree":
		apiMftTree(c, user)
	case endpoint == "mft/scan":
		apiMftScan(c, user)
	case endpoint == "mft/mtimes":
		apiMftMtimes(c, user)
	case endpoint == "explorer/open":
		writeError(c, http.StatusGone, "「打开资源管理器」是 zip-browser 的本地 Windows 功能，在 OpenList 中已移除")
	default:
		writeError(c, http.StatusNotFound, "unknown endpoint")
	}
}

func userJSON(user *model.User) gin.H {
	if user == nil {
		return gin.H{"login": false}
	}
	return gin.H{
		"login":    true,
		"username": user.Username,
		"guest":    user.IsGuest(),
		"admin":    user.IsAdmin(),
		"write":    user.CanWriteContent(),
		"remove":   user.CanRemove(),
		"archive":  user.CanReadArchives(),
	}
}

func handleSession(c *gin.Context, user *model.User) {
	writeJSON(c, http.StatusOK, gin.H{
		"user":    userJSON(user),
		"version": conf.Version,
		"base":    baseURL(),
	})
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func handleLogin(c *gin.Context) {
	var req loginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		writeError(c, http.StatusBadRequest, "请输入用户名")
		return
	}
	user, err := op.GetUserByName(req.Username)
	if err != nil {
		writeError(c, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	if err := user.ValidateRawPassword(req.Password); err != nil {
		writeError(c, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	if user.Disabled {
		writeError(c, http.StatusForbidden, "该用户已被停用")
		return
	}
	token, err := generateSessionToken(user)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	path := strings.TrimSuffix(conf.URL.Path, "/")
	if path == "" {
		path = "/"
	}
	c.SetCookie(tokenCookie, token, int(zbSessionTTL.Seconds()), path, "", false, true)
	writeJSON(c, http.StatusOK, gin.H{"token": token, "user": userJSON(user)})
}

func handleLogout(c *gin.Context) {
	path := strings.TrimSuffix(conf.URL.Path, "/")
	if path == "" {
		path = "/"
	}
	c.SetCookie(tokenCookie, "", -1, path, "", false, true)
	writeJSON(c, http.StatusOK, gin.H{"ok": true})
}

func joinUser(user *model.User, p string) (string, error) {
	return user.JoinPath(normalizeVirtual(p))
}

func apiDrives(c *gin.Context, user *model.User) {
	reqPath, err := user.JoinPath("/")
	if err != nil {
		writeError(c, http.StatusForbidden, err.Error())
		return
	}
	objs, err := fs.List(c.Request.Context(), reqPath, &fs.ListArgs{Refresh: true, WithStorageDetails: false, NoLog: false})
	if err != nil {
		// 全新实例还没有挂载任何存储时，OpenList 会返回 storage not found；
		// 这里当作空列表交给前端显示引导文案，而不是当成错误。
		if isStorageNotFound(err) {
			writeJSON(c, http.StatusOK, gin.H{"drives": make([]driveItem, 0)})
			return
		}
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	drives := make([]driveItem, 0, len(objs))
	for _, obj := range objs {
		if !obj.IsDir() {
			continue
		}
		display := joinVirtual("/", obj.GetName())
		provider := ""
		if d, err := fs.GetStorage(stdpath.Join(reqPath, obj.GetName()), &fs.GetStoragesArgs{}); err == nil && d != nil {
			provider = d.Config().Name
		}
		drives = append(drives, driveItem{
			Label:   obj.GetName(),
			Path:    display,
			Kind:    provider,
			Virtual: true,
		})
	}
	writeJSON(c, http.StatusOK, gin.H{"drives": drives})
}

func apiList(c *gin.Context, user *model.User) {
	raw := c.Query("path")
	refresh := c.Query("refresh") == "1" || c.Query("refresh") == "true"
	p := normalizeVirtual(raw)
	display := p
	reqPath, err := user.JoinPath(p)
	if err != nil {
		writeError(c, http.StatusForbidden, err.Error())
		return
	}
	meta, metaErr := op.GetNearestMeta(reqPath)
	if metaErr != nil && !errors.Is(errors.Cause(metaErr), errs.MetaNotFound) {
		writeError(c, http.StatusInternalServerError, metaErr.Error())
		return
	}
	common.GinAppendValues(c, conf.MetaKey, meta)
	if !common.CanAccess(user, meta, reqPath, "") {
		writeError(c, http.StatusForbidden, "该目录受密码保护，zip-browser 前端暂不支持目录密码")
		return
	}
	objs, err := fs.List(c.Request.Context(), reqPath, &fs.ListArgs{
		Refresh:            refresh,
		WithStorageDetails: false,
	})
	if err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]listItem, 0, len(objs))
	names := make([]string, 0, len(objs))
	for _, obj := range objs {
		name := obj.GetName()
		names = append(names, name)
		item := listItem{
			Name:  name,
			Path:  joinVirtual(display, name),
			Dir:   obj.IsDir(),
			Size:  obj.GetSize(),
			Mtime: obj.ModTime().UnixMilli(),
			Zip:   !obj.IsDir() && isArchiveName(name),
		}
		item.Archive = item.Zip
		if item.Zip {
			item.Encrypted = "unknown"
		}
		items = append(items, item)
	}
	writeJSON(c, http.StatusOK, gin.H{
		"path":  display,
		"items": items,
		"tags":  detectTags(names),
	})
}

func apiDownload(c *gin.Context, user *model.User) {
	p := normalizeVirtual(c.Query("path"))
	reqPath, err := user.JoinPath(p)
	if err != nil {
		writeError(c, http.StatusForbidden, err.Error())
		return
	}
	obj, err := fs.Get(c.Request.Context(), reqPath, &fs.GetArgs{})
	if err != nil {
		writeError(c, http.StatusNotFound, err.Error())
		return
	}
	if obj.IsDir() {
		writeError(c, http.StatusBadRequest, "目录不能直接下载")
		return
	}
	serveDownload(c, reqPath, obj, baseName(p))
}

func apiHostInfo(c *gin.Context, user *model.User) {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "openlist"
	}
	volumes := map[string]string{}
	reqPath, err := user.JoinPath("/")
	if err == nil {
		if objs, err := fs.List(c.Request.Context(), reqPath, &fs.ListArgs{Refresh: true}); err == nil {
			for _, obj := range objs {
				if !obj.IsDir() {
					continue
				}
				volumes[obj.GetName()] = "openlist::" + obj.GetName()
			}
		}
	}
	writeJSON(c, http.StatusOK, gin.H{
		"host":     host,
		"volumes":  volumes,
		"platform": "openlist",
	})
}

func apiVersion(c *gin.Context, user *model.User) {
	writeJSON(c, http.StatusOK, gin.H{
		"version": conf.Version,
		"web":     conf.WebVersion,
		"name":    "OpenList",
	})
}

type trashReq struct {
	Dir   string   `json:"dir"`
	Paths []string `json:"paths"`
}

func apiTrash(c *gin.Context, user *model.User) {
	if !user.CanRemove() {
		writeError(c, http.StatusForbidden, "当前用户没有删除权限")
		return
	}
	var req trashReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	dir := normalizeVirtual(req.Dir)
	reqDir, err := user.JoinPath(dir)
	if err != nil {
		writeError(c, http.StatusForbidden, err.Error())
		return
	}
	meta, metaErr := op.GetNearestMeta(reqDir)
	if metaErr != nil && !errors.Is(errors.Cause(metaErr), errs.MetaNotFound) {
		writeError(c, http.StatusInternalServerError, metaErr.Error())
		return
	}
	common.GinAppendValues(c, conf.MetaKey, meta)
	if !common.CanWrite(user, meta, reqDir) {
		writeError(c, http.StatusForbidden, "当前目录不允许删除")
		return
	}
	if len(req.Paths) > 50 {
		writeError(c, http.StatusBadRequest, "一次最多删除 50 项")
		return
	}
	results := make([]gin.H, 0, len(req.Paths))
	for _, raw := range req.Paths {
		p := normalizeVirtual(raw)
		if parentVirtual(p) != dir {
			results = append(results, gin.H{"path": p, "ok": false, "error": "只能删除当前目录下的项目"})
			continue
		}
		target := stdpath.Join(reqDir, baseName(p))
		if err := fs.Remove(c.Request.Context(), target); err != nil {
			results = append(results, gin.H{"path": p, "ok": false, "error": err.Error()})
			continue
		}
		results = append(results, gin.H{"path": p, "ok": true})
	}
	allOK := true
	for _, row := range results {
		if ok, _ := row["ok"].(bool); !ok {
			allOK = false
			break
		}
	}
	writeJSON(c, http.StatusOK, gin.H{"ok": allOK, "results": results})
}

func apiMftMtimes(c *gin.Context, user *model.User) {
	var req struct {
		Paths []string `json:"paths"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	if len(req.Paths) > 5000 {
		req.Paths = req.Paths[:5000]
	}
	out := make(map[string]int64, len(req.Paths))
	for _, raw := range req.Paths {
		p := normalizeVirtual(raw)
		reqPath, err := user.JoinPath(p)
		if err != nil {
			continue
		}
		obj, err := fs.Get(c.Request.Context(), reqPath, &fs.GetArgs{})
		if err != nil {
			continue
		}
		out[p] = obj.ModTime().UnixMilli()
	}
	writeJSON(c, http.StatusOK, out)
}

// isStorageNotFound reports whether the error simply means “no storage mounted
// yet”, which a fresh install always hits before the first mount is added.
func isStorageNotFound(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, errs.StorageNotFound) ||
		errors.Is(errors.Cause(err), errs.StorageNotFound) ||
		strings.Contains(strings.ToLower(err.Error()), "please add a storage first")
}

func detectTags(names []string) []string {
	tags := make([]string, 0, 4)
	seen := map[string]bool{}
	for _, name := range names {
		tag, ok := tagRules[name]
		if !ok || seen[tag] {
			continue
		}
		seen[tag] = true
		tags = append(tags, tag)
	}
	if tags == nil {
		tags = []string{}
	}
	return tags
}

var tagRules = map[string]string{".git": "git", ".gitignore": "git", ".gitmodules": "git",
	"node_modules": "node_modules", "package.json": "node", "package-lock.json": "npm",
	"yarn.lock": "yarn", "pnpm-lock.yaml": "pnpm", "pnpm-workspace.yaml": "pnpm",
	"Cargo.toml": "rust", "Cargo.lock": "rust",
	"requirements.txt": "python", "pyproject.toml": "python", "setup.py": "python",
	".venv": "venv", "venv": "venv", ".svn": "svn", ".hg": "hg",
	".idea": "idea", ".vscode": "vscode",
	"Dockerfile": "docker", "docker-compose.yml": "docker", "docker-compose.yaml": "docker",
	"go.mod": "go", "go.sum": "go",
	"Gemfile": "ruby", "Gemfile.lock": "ruby",
	"pom.xml": "maven", "build.gradle": "gradle", "build.gradle.kts": "gradle",
	"Makefile": "make", "CMakeLists.txt": "cmake",
}
