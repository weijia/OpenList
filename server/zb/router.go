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
	auth := "1"
	username := ""
	if user != nil && !user.IsGuest() {
		username = user.Username
	}
	page := string(content)
	page = strings.ReplaceAll(page, "__ZB_BASE_VALUE__", jsString(baseURL()))
	page = strings.ReplaceAll(page, "__ZB_TITLE_VALUE__", html.EscapeString(title))
	page = strings.ReplaceAll(page, "__ZB_VERSION_VALUE__", jsString(conf.Version))
	page = strings.ReplaceAll(page, "__ZB_AUTH_VALUE__", auth)
	page = strings.ReplaceAll(page, "__ZB_USER_VALUE__", jsString(username))
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
