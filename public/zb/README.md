# public/zb：构建时生成，勿手工维护

`/zb` 兼容层的前端不再是 OpenList 的 fork 副本，而是 **zip-browser** 的宿主无关发布产物：

- 源码与问题修复在 <https://github.com/weijia/zip-browser>
- 发布产物：该仓库 Release 中的 `zip-browser-web.tar.gz`
- 构建脚本：`build.sh` 的 `FetchZbDist`（`ZB_REPO` 可覆盖仓库）

## 本地构建

```bash
bash build.sh dev web   # 或 release / beta；会下载前端 dist 与 zb dist
```

只跑 `go build` 而没下载前端时，`public/zb` 只有本文件，`/zb` 会返回 500。这是预期行为：
`//go:embed all:zb` 需要一个占位文件才能编译，运行资产由构建步骤补齐。

## 自定义仓库

```bash
ZB_REPO=your-name/zip-browser bash build.sh dev web
```

`FetchZbDist` 下载失败时会保留现有 `public/zb`，避免离线构建被卡死；需要强制更新时先删除该目录再构建。

## 私有仓库

默认的 `weijia/zip-browser` 目前是私有仓库。构建机需要提供 `GITHUB_TOKEN`（`repo` 权限），
脚本会同时用它读取 Release 元数据和下载资产；仓库转为公开后无需 token 也能匿名下载。
