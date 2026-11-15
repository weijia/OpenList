# zip-browser 前端 × OpenList 后端 集成说明

把 `zip-browser` 的浏览器前端挂到 OpenList 上，由 Go 侧实现原 Node 服务端的 HTTP 契约，
前端不再依赖本地磁盘与 `X-Local-Token`，改为走 OpenList 的虚拟路径、账号与权限。

## 1. 目录与入口

| 位置 | 作用 |
| --- | --- |
| `server/zb/` | 兼容层 Go 包：路由、API、内容服务、压缩包、缩略图、清单扫描、预览渲染 |
| `public/zb/` | 前端静态资源（`index.html`、`app.js`、`vault.js`、`vendor/` 等），随二进制 `go:embed` 打包 |
| `server/router.go` | 注册 `zb.Register(g)` |
| `server/static/static.go` | `public/dist` 无 `index.html` 时，`/` 返回跳转到 `zb/` 的 launcher |
| `public/public.go` | `//go:embed all:zb` + `var Zb embed.FS` |

入口：`http://<host>:<port><URL.Path>/zb/`，`/` 会跳到该地址。

## 2. 前端 ↔ 后端契约

所有接口都在 `conf.URL.Path + /zb` 之下，避免与 OpenList 原生 `/api/*` 冲突。

### 认证

- `POST /zb/api/login` `{username,password}` → `{token,user:{username,admin,guest,write,remove,archive,login}}`，同时写 `HttpOnly` Cookie `zb_token`（有效期 7 天，路径为 `conf.URL.Path`）。
- 前端把 token 也留在 `localStorage.zb-token`，请求头带上；Cookie 供 `iframe` / 缩略图 / 直链下载使用。
- `POST /zb/api/logout` 清 Cookie。
- 权限完全沿用 OpenList：游客被禁用时未登录访问返回 `401 {"error":"游客访问已关闭，请登录"}`，前端据此弹出登录框。

### 路径模型

- 原 `C:\dir\file` → OpenList 虚拟路径 `/存储名/dir/file`。前端 `location.js` 的 `basename/dirname` 已改为 POSIX 语义。
- 「此电脑」根视图 = `GET /zb/api/drives` 返回的挂载列表（所有 OpenList 驱动都可用），`GET /zb/api/list?path=/` 也能列出根下的挂载目录。
- 路径一律经 `user.JoinPath` 归一化，越权路径返回 403。

### 接口清单

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/zb/api/session` | 当前用户与版本（`base` 指向前端根） |
| POST | `/zb/api/login` / `/zb/api/logout` | 登录 / 登出 |
| GET | `/zb/api/drives` | 存储挂载列表（`label/path/kind/total/free`） |
| GET | `/zb/api/list?path=&refresh=` | 目录列表（`dir/size/mtime/archive/zip/encrypted/tags`） |
| GET | `/zb/api/download?path=` | 原样下载（`Content-Disposition: attachment`） |
| GET | `/zb/api/hostinfo` | 清单用的主机信息（`volumes` 为「存储名 → openlist::存储名」） |
| GET | `/zb/api/version` | 版本信息（前端更新检查） |
| POST | `/zb/api/trash` | 删除（OpenList 删除语义，见下） |
| POST | `/zb/api/zip/open` | 打开压缩包（`{path,password}`，返回 `id/entries/encrypted/needsPassword`） |
| POST | `/zb/api/zip/unlock` | 用密码重新读取条目；密码错误返回 `401 密码不正确` |
| GET | `/zb/api/zip/tree?id=` | 压缩包条目树 |
| POST | `/zb/api/zip/close` | 关闭会话（30 分钟 TTL 自动回收） |
| GET | `/zb/api/mft/tree?path=` | 清单扫描用的目录树（只含目录 + tags） |
| POST | `/zb/api/mft/scan` | 递归扫描（`{paths,recursive,timeoutMs}`，返回 `files` 分桶） |
| POST | `/zb/api/mft/mtimes` | 批量取 `mtime` |
| GET | `/zb/fs/*` | 目录页 / 文件预览页；`?raw=1` 取原始字节；`?view=thumbs` 缩略图墙 |
| GET | `/zb/zip/{id}/{entry}` | 压缩包内条目页；`?raw=1` 取原始字节 |
| GET | `/zb/thumb/fs/...`、`/zb/thumb/zip/...`、`/zb/thumb/zipcover/{fs|zip}/...` | 缩略图（160px，JPEG） |
| GET | `/zb/api/explorer/open` | 已移除，固定返回 `410`（Windows 资源管理器专属） |

浏览语义：带 `Sec-Fetch-Mode: navigate` / `Sec-Fetch-Dest: document` 的请求返回 HTML 预览页
（markdown 渲染、图片查看器、纯文本高亮），否则返回原始字节，便于 `img`/`iframe`/下载复用同一路径。

## 3. 功能对照

| zip-browser 原功能 | OpenList 上的实现 |
| --- | --- |
| 盘符列表（C:\、D:\） | 「存储挂载」卡片：`/zb/api/drives`，覆盖所有 OpenList 驱动（本地、WebDAV、S3、网盘…） |
| 目录浏览 | `fs.List` + 虚拟路径；面包屑根节点文案为「存储挂载」 |
| 缩略图 | 服务端取流后缩放成 160px JPEG，缓存 300 条 / 32MB 上限源文件；本地驱动无直链时走 RangeReader |
| 压缩包浏览（zip/7z/rar/tar…） | `fs.ArchiveMeta` + `fs.ArchiveInternalExtract`，会话内存缓存；条目路径已归一为 `/` |
| 加密压缩包 | `needsPassword` → 前端缺省密码 / 密码库自动尝试 → 手动输入；后端 `zip/unlock` 会真实试解一个条目校验密码 |
| 文件预览 | markdown / 文本 / 图片查看器，服务端渲染 HTML |
| 书签 / 标签 | 前端 `annotations-store.js`（浏览器本地存储），无需服务端接口 |
| 清单生成（v2） | 前端 `manifest-builder/export/sync`；服务端提供 `hostinfo`、`mft/tree`、`mft/scan`、`mft/mtimes`；`hostRelativePath` 首段=存储名，形如 `LOCAL:/photos` |
| 删除 | `POST /zb/api/trash` → `fs.Remove`：本地驱动是否进回收站由驱动/挂载配置决定，其它驱动通常永久删除；仅允许删除当前目录下的同级项目，单批 ≤50 |
| 打开资源管理器 | 已移除（`410`） |
| KeePass 密码库 | 浏览器端 `kdbxweb` + `hash-wasm`（Argon2），`vault.js` 对外签名与 `app.js` 兼容；库文件不上传服务端 |

## 4. 已知降级点（与原 zip-browser 的差异）

1. **加密徽标**：`apiList` 的 `encrypted` 恒为字符串 `"unknown"`（前端只在 `true` 时显示 🔒），列表不显示加密标记；是否加密在打开压缩包时的 `needsPassword` 上体现。
2. **目录密码**：`fs` 层支持 OpenList 目录密码，但 `/zb` 页面没有密码输入入口；受保护目录在 `apiList` 返回 `403 该目录受密码保护，zip-browser 前端暂不支持目录密码`。
3. **资源管理器**：`/zb/api/explorer/open` 返回 `410`，前端按钮已删除。
4. **清单 v2 的卷语义**：由 Windows 盘符改为「首段=存储名」（`volumes[upper] ?? volumes[name] ?? volumes[drive]`），与 `apiHostInfo` 的映射一致。
5. **密码库封面**：服务端不再读取密码库内容，`zipcover` 只能用「缺省解压密码」`?defpw=`；密码库按路径保存的密码只在手动输入/选择密码时使用，`getVaultSessionId()` 恒返回 `null`。
6. **版本信息字段**：`apiVersion` 返回 `{version, web, name}`，而 `update-checker.js` 读 `info.webVersion`，只影响更新提示文案。

## 5. 构建与运行

本仓库 `go.mod` 要求 `go 1.26.4` + `toolchain go1.27.3`（依赖 `github.com/go-gfx/gfx` 需要 ≥1.26.4）。
构建前需就位 Go 1.27.3，并设置：

```powershell
$env:GOROOT='C:\codes\_gosdk\go'
$env:PATH="$env:GOROOT\bin;"+$env:PATH
$env:GOTOOLCHAIN='local'
$env:GOPROXY='https://mirrors.huaweicloud.com/repository/goproxy/,direct'
$env:GOSUMDB='off'
$env:CGO_ENABLED='0'
go build -o openlist_zb.exe .
```

运行：

```powershell
.\openlist_zb.exe server --data <数据目录>    # 端口取自 <数据目录>\config.json 的 scheme.http_port
```

首次运行没有 admin 账号时（`initAdmin` 只在 `flags.Dev` 或存在 `OPENLIST_ADMIN_PASSWORD` 时创建），
设置 `OPENLIST_ADMIN_PASSWORD=xxx` 启动一次即可建号，或走 Web 安装向导 `POST /api/public/init/setup`。

## 6. 冒烟验证记录（Local 驱动，端口 5288）

已验证通过：`/` launcher 跳转、`/zb/` 占位符替换、登录/登出与 Cookie、`drives`、`list`（含子目录）、
`hostinfo`、`mft/tree`、`mft/scan`、`mft/mtimes`、`download`、`fs` 目录页 / 文本原文 / 导航模式预览页、
`thumb`（图片与压缩包封面）、`zip/open` → `zip/{id}/` → 根条目与嵌套条目（`sub/inner.txt`）预览与 `?raw=1`、
`zip/unlock` 错误密码 `401` 与正确密码成功、`trash` 删除落盘、前端资源与 `vendor/*` 全部 200。
前端在无头 Chrome 中可正常渲染并触发 `401 → 登录框`，`node --check` 全部模块语法通过。

**未覆盖**：真实浏览器里登录后的完整交互（书签/标签本地存储、清单生成落盘、kdbxweb 建库与解锁全流程）、
7z/rar 口令与分卷包、目录密码、远程驱动（WebDAV/S3）的缩略图与删除行为。

## 7. 自动挂载所有本地磁盘根目录

启动流程新增 `bootstrap.EnsureLocalDriveStorages()`（`internal/bootstrap/storage.go`，在 `bootstrap.Start()` 里于 `LoadStorages()` 之前调用）：

- 枚举本机所有已存在的盘符根目录（Windows 为 `C:\`、`D:\` …；Unix 为 `/`），逐盘建立 Local 存储；
- 挂载路径取盘符：`C:\` → `/C`，`O:\` → `/O`；`remark=自动挂载本地磁盘`，`root_folder_path` 就是盘符根；
- 只写 DB 不实例化驱动，随后由 `LoadStorages()` 统一加载，避免重复注册；
- 一次性：首次成功后写标记文件 `<data>\auto-mount.done`，之后启动直接跳过，
  既不新增盘也不删除/修改任何挂载；另加一道保险——数据库里若已有非本功能创建的存储，
  同样立即退出，不干预用户手工配置；
- 兼容迁移：若发现早期版本留下的 `/local -> <程序目录>\files` 自动挂载，会删除该记录再按盘符重建。

验证（端口 5244，真实二进制，本机存在 C:\ 与 O:\）：

    POST /zb/api/login (admin/admin888)                 -> 200
    GET  /zb/api/drives  Authorization: Bearer <token>  -> 200 {"drives":[{label:C,path:/C},{label:O,path:/O}]}
    GET  /zb/api/list?path=/C                           -> 200，26 项（C 盘根目录真实内容）
    GET  /zb/api/list?path=/O                           -> 200，16 项（O 盘根目录真实内容）
    日志：removed stale auto storage [/local] -> ...\files
          created auto storage [/C] -> C:\ (id=2)
          created auto storage [/O] -> O:\ (id=3)
          success load storage: [/C] / [/O]，driver: [Local]

## 8. 认证修复（Bearer 前缀）

`resolveUser` 之前先取 `Authorization` 头，非空就不再看 Cookie，而前端发的是
`Authorization: Bearer <token>`，整串送进 `common.ParseToken` 必然失败 → 登录成功后刷新仍 401。
现已加 `bearerToken()`：头部与 Cookie 值都先剥离 `Bearer ` 前缀（大小写不敏感），裸 token 同样接受。
前端 `submitLogin()` 的 `data.user` 是对象，写 localStorage 会变成 `[object Object]`，已改为取 `user.username`。

## 9. 官方后台前端与本包关系

原始仓库的 `public/dist` 只有 README，等于没有后台前端，`/@manage` 会被 launcher 重定向到 `/zb/`。
现已把官方前端（`OpenList-Frontend` v4.2.9，`openlist-frontend-dist-v4.2.9.tar.gz`）解压进
`public/dist`，由 `public.Public` 的 `//go:embed all:dist` 编进 exe，运行时 `dist_dir` 留空即用内嵌资源。
因此运行包不需要外部 `web\` 目录（该临时目录已删除），exe 从 143MB 增至 202MB：

- `GET /@manage` -> 200，返回真正的 SPA（`/assets/index-legacy-*.js` 也是 200）；
- `GET /api/public/settings` -> 200，前后端可正常联调；
- `GET /zb/` -> 200，zip-browser 前端不受影响；
- 站点根 `/` 现在返回官方前端首页（不再是 launcher 跳转），文件浏览器仍走 `/zb/`；
- zb 顶栏新增「管理后台」按钮（`#adminBtn`，`app.js` 里从 BASE 去掉 `/zb` 后缀拼 `/@manage`，新标签打开）。

注意：前端 v4.2.9 与后端 dev 版本可能存在接口差异，本次验证覆盖的是页面/静态资源/公共设置接口。

## 10. 会话持久化（zb 登录记住一个月）

问题：`server/common/auth.go` 的 `ParseToken` 除了校验签名，还要求 token 存在于内存缓存
`validTokenCache`（`go-cache`）。这个缓存只活在进程里，服务一重启全部清空，于是所有旧 token
被判 `token is invalidated`，表现为「每次重启程序都要重新登录」；同时 token 有效期来自
`conf.Conf.TokenExpiresIn`（本实例 48），Cookie 虽给 7 天，48 小时后也会过期。

改动：

- `server/zb/router.go` 新增 `parseSessionToken()`：用 `jwt.ParseWithClaims` + `common.SecretKey`
  只校签名与 `exp`，不再查内存缓存；`resolveUser` 改用它。密码一旦修改，`user.PwdTS` 与 claims
  不一致，旧 token 仍然立即失效。
- `server/zb/api.go` 新增 `generateSessionToken()`：签发 30 天 token（`zbSessionTTL = 30*24h`），
  Cookie `zb_token` 的 `Max-Age` 同步改为 `2592000`。
- `public/zb/app.js`：`SESSION_USER` 服务端未注入时回退读 `localStorage['zb-user']` 展示，
  并忽略历史的 `[object Object]` 脏值。

验证（真实请求）：

- `POST /zb/api/login` -> 200，`Set-Cookie: zb_token=...; Max-Age=2592000; HttpOnly`，
  JWT `exp - iat = 30 天`；
- 同一 token 在 `kill + 重启` 前后访问 `/zb/api/drives` 均 200（`Bearer` 与裸 token 都通过）；
- 带 `Cookie: zb_token=<token>` 打开 `/zb/`，页面注入 `__ZB_USER__ = "admin"`。

注意：管理后台走的是原生 `/api/auth/login`，仍是 48 小时 token 且校验同一条内存缓存，
重启后同样会掉登录；如需要，可把 `data/config.json` 的 `token_expires_in` 调大。