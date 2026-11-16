package zb

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"html"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/public"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
)

const tokenCookie = "zb_token"

// Register mounts the zip-browser frontend and its compatibility API under /zb.
func Register(g *gin.RouterGroup) {
	group := g.Group("/zb")
	group.GET("", func(c *gin.Context) {
		c.Redirect(http.StatusFound, baseURL()+"/")
	})
	group.Any("/*path", handle)
}

func handle(c *gin.Context) {
	user := resolveUser(c)
	if user != nil {
		common.GinAppendValues(c, conf.UserKey, user)
	}
	raw := strings.TrimPrefix(c.Param("path"), "/")
	switch {
	case raw == "" || raw == "index.html":
		serveIndex(c, user)
	case strings.HasPrefix(raw, "api/"):
		handleAPI(c, user, strings.TrimPrefix(raw, "api/"))
	case strings.HasPrefix(raw, "fs/"):
		handleContent(c, user, "/"+strings.TrimPrefix(raw, "fs/"))
	case raw == "fs":
		handleContent(c, user, "/")
	case strings.HasPrefix(raw, "zip/"):
		handleZipContent(c, user, strings.TrimPrefix(raw, "zip/"))
	case strings.HasPrefix(raw, "thumb/"):
		handleThumb(c, user, strings.TrimPrefix(raw, "thumb/"))
	default:
		serveAsset(c, raw)
	}
}

func resolveUser(c *gin.Context) *model.User {
	token := bearerToken(c.GetHeader("Authorization"))
	if token == "" {
		if cookie, err := c.Cookie(tokenCookie); err == nil {
			token = bearerToken(cookie)
		}
	}
	if token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(setting.GetStr(conf.Token))) == 1 {
		admin, err := op.GetAdmin()
		if err != nil {
			return nil
		}
		return admin
	}
	if token == "" {
		guest, err := op.GetGuest()
		if err != nil {
			return nil
		}
		return guest
	}
	claims, err := parseSessionToken(token)
	if err != nil {
		return nil
	}
	user, err := op.GetUserByName(claims.Username)
	if err != nil {
		return nil
	}
	if user.PwdTS != claims.PwdTS || user.Disabled {
		return nil
	}
	return user
}

// parseSessionToken validates a zb session token by signature and expiry only.
// OpenList's common.ParseToken additionally requires the token to be present in
// an in-memory cache, which is wiped on every restart, so every restart forced
// a new login. zb sessions are long lived instead: logout still clears the
// cookie and localStorage, and a password change bumps PwdTS which invalidates
// the old token here.
func parseSessionToken(token string) (*common.UserClaims, error) {
	claims := &common.UserClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		return common.SecretKey, nil
	})
	if err != nil {
		return nil, err
	}
	if !parsed.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

// bearerToken accepts both the raw token and the standard "Bearer <token>"
// form, so the browser client and plain API callers behave the same.
func bearerToken(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 7 && strings.EqualFold(value[:7], "bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return value
}

// requireUser returns false and writes a 401 when nobody is logged in.
func requireUser(c *gin.Context, user *model.User) bool {
	if user == nil {
		writeError(c, http.StatusUnauthorized, "未登录，请先登录")
		return false
	}
	if user.IsGuest() && user.Disabled {
		writeError(c, http.StatusUnauthorized, "游客访问已关闭，请登录")
		return false
	}
	return true
}

func writeJSON(c *gin.Context, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		c.String(http.StatusInternalServerError, "encode failed")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(status, "application/json; charset=utf-8", body)
}

func writeError(c *gin.Context, status int, message string) {
	writeJSON(c, status, gin.H{"error": message, "ok": false})
}

func serveIndex(c *gin.Context, user *model.User) {
	content, err := fs.ReadFile(public.Zb, "zb/index.html")
	if err != nil {
		c.String(http.StatusInternalServerError, "index missing")
		return
	}
	title := "文件浏览器"
	if siteTitle := setting.GetStr(conf.SiteTitle); siteTitle != "" {
		title = siteTitle
	}
	username := ""
	isAdmin := false
	canRemove := false
	if user != nil && !user.IsGuest() {
		username = user.Username
		isAdmin = user.IsAdmin()
		canRemove = user.CanRemove()
	}
	host := map[string]any{
		"basePath":        baseURL(),
		"auth":            "bearer",
		"pathStyle":       "posix",
		"drivesMode":      "mounts",
		"vaultMode":       "browser",
		"versionEndpoint": "/api/version",
		"versionKind":     "openlist",
		"versionValue":    conf.Version,
		"siteTitle":       title,
		"user":            username,
		"labels": map[string]any{
			"root":         "存储挂载",
			"mode":         "存储挂载",
			"drivesTitle":  "存储挂载",
			"drivesUnit":   "存储",
			"drivesEmpty":  "当前没有挂载任何存储，请先在 OpenList 管理后台添加存储",
			"drivesHint":   "这里是 OpenList 已挂载存储的列表（本地磁盘 / WebDAV / S3 / 网盘等）；点击卡片进入目录。遇到 zip / 7z / rar / tar.gz 等压缩包可直接点开，加密压缩包会提示输入密码（可自动用缺省密码或密码库记录尝试）。进入目录后可用行内「删除」或勾选批量删除，删除行为由 OpenList 按驱动能力执行（本地存储可落回收站，其它驱动通常永久删除）。",
			"sidebarRoot":  "存储挂载",
			"previewTitle": "文件预览",
			"previewHint":  "相对引用按目录解析",
			"trashHint":    "删除由 OpenList 执行：本地存储开启回收站时可还原，网盘/对象存储通常永久删除",
			"trashAction":  "确认删除",
			"trashWarning": "⚠️ 删除会调用 OpenList 的删除接口：本地存储开启回收站时移入回收站，其它驱动（网盘 / 对象存储 / WebDAV）通常是永久删除；文件夹会连同内部全部内容一起删除。",
			"trashNote":    "删除权限由 OpenList 用户角色与目录权限决定；受密码保护的目录暂不支持在本页删除。",
			"trashStatus":  "OpenList 多存储 · 删除按驱动能力 · ",
		},
		"features": map[string]any{
			"explorer": false,
			"encoding": false,
			"admin":    isAdmin,
			"remove":   canRemove,
			"login":    true,
		},
	}
	hostJSON, err := json.Marshal(host)
	if err != nil {
		c.String(http.StatusInternalServerError, "host config failed")
		return
	}
	hostJSON = bytes.ReplaceAll(hostJSON, []byte("<"), []byte(`\u003c`))

	page := string(content)
	// 保留旧版占位符替换，便于继续服务仍带 __ZB_* 的历史 index.html。
	page = strings.ReplaceAll(page, "__ZB_BASE_VALUE__", jsString(baseURL()))
	page = strings.ReplaceAll(page, "__ZB_TITLE_VALUE__", html.EscapeString(title))
	page = strings.ReplaceAll(page, "__ZB_VERSION_VALUE__", jsString(conf.Version))
	page = strings.ReplaceAll(page, "__ZB_AUTH_VALUE__", "1")
	page = strings.ReplaceAll(page, "__ZB_USER_VALUE__", jsString(username))
	// 自带 Node 服务用的注入点，在 OpenList 下必须替换成合法 JS 值。
	page = strings.ReplaceAll(page, "__ZIP_BROWSER_TOKEN__", "")
	page = strings.ReplaceAll(page, "__ZIP_BROWSER_HOST__", "")
	versionJSON, _ := json.Marshal(conf.Version)
	page = strings.ReplaceAll(page, "__ZIP_BROWSER_VERSION__", string(versionJSON))
	page = strings.Replace(page, "<head>", "<head>\n<script>window.ZB_HOST="+string(hostJSON)+";</script>", 1)
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(page))
}

// jsString renders a Go string as the inside of a double-quoted JavaScript
// literal, so titles and usernames carrying quotes or backslashes stay valid.
func jsString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(string(encoded), `"`), `"`)
}

func serveAsset(c *gin.Context, name string) {
	clean := path.Clean("/" + strings.ReplaceAll(name, "\\", "/"))
	if clean == "/" || strings.Contains(clean, "..") {
		c.Status(http.StatusNotFound)
		return
	}
	rel := strings.TrimPrefix(clean, "/")
	if !strings.HasPrefix(rel, "vendor/") {
		switch extensionOf(rel) {
		case ".html", ".js", ".css", ".map":
		default:
			if baseName(rel) != "favicon.ico" && baseName(rel) != "manifest.json" {
				c.Status(http.StatusNotFound)
				return
			}
		}
	}
	content, err := fs.ReadFile(public.Zb, "zb/"+rel)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Content-Type", mimeOf(rel))
	c.Header("Cache-Control", "no-cache")
	http.ServeContent(c.Writer, c.Request, baseName(rel), time.Time{}, bytes.NewReader(content))
}
