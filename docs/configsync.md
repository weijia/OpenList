# 配置同步（configsync）

把 OpenList 的一部分设置保存到 [zen-fs-config](https://github.com/zen-fs/config)
兼容的「配置同步组」仓库，并在启动时从仓库拉取应用。字节格式与 JS 版
`zen-fs-config` 完全一致，同一个仓库可以被 OpenList（Go）和 JS 客户端交替读写。

实现位于 `internal/configsync`，依赖已发布的
`github.com/weijia/go-zenfs-config v0.1.0`（MIT，公开仓库，可直接 `go get`）。
`go.mod` 不再使用本地 `replace`。

## 启用

在数据目录（`--data` 指定的目录，默认 `data`）创建 `configsync.json`：

```json
{
  "enabled": true,
  "gitee": {
    "owner": "you",
    "repo": "configs",
    "branch": "master",
    "token": "GITEE_TOKEN"
  },
  "app_id": "openlist",
  "prefix": "/openlist",
  "include_storages": true,
  "group_type": "auto",
  "data_group": "",
  "data_group_name": "OpenList",
  "app_data": {
    "enabled": false,
    "repo": "openlist-data",
    "branch": "master",
    "group_id": "default",
    "name": "OpenList"
  },
  "push_debounce_seconds": 5
}
```

- `token` 留空时读取环境变量 `GITEE_TOKEN`；
- 也可以用环境变量 `OPENLIST_CONFIGSYNC` 指向另一个配置文件路径；
- 没有该文件或 `enabled=false` 时功能完全关闭，不会访问网络。

### 组类型自动判断

`group_type` 默认 `auto`，按远端标记决定：

1. `/.config/group-type`
2. `/.meta/group-type`
3. `.meta/group.json` 的 `kind`

只接受 `config-sync` / `data-sync`。标记值无法识别时不会猜测，直接报错并停止同步。
远端完全没有标记时按 `config-sync` 处理；空仓库会自动初始化成 config 组。

也可以显式写成 `"group_type": "data-sync"` 强制按数据组管理（此时远端 marker
缺失也会创建数据组描述符）。配置与远端类型不一致会报
`group type mismatch`，不会向错误类型的仓库写入。

- **config-sync**：沿用 `/{prefix}/settings/*.json` + 镜像 Push/Pull；
- **data-sync**：直接通过数据组的版本化文件 API 读写同一批路径，
  不把整个数据组镜像到本地；`data_group` 留空时读 `.meta/group.json` 的 `id`。

`app_data` 用于在 config 组里登记一个数据组（引导副本
`/.config/app-data-groups/{app_id}/{group_id}.json`），让其他设备只凭 config 组
就能发现数据组。数据组自身 `.meta/backends/*.json` 仍是权威来源。

建议把配置文件权限收紧，或只使用环境变量提供 token。

## 同步范围

默认只导出以下设置分组（`internal/model/setting.go` 中的常量）：

| 分组 | 文件 |
| --- | --- |
| `SITE` | `/{prefix}/settings/site.json` |
| `STYLE` | `/{prefix}/settings/style.json` |
| `PREVIEW` | `/{prefix}/settings/preview.json` |
| `INDEX` | `/{prefix}/settings/index.json` |
| `TRAFFIC` | `/{prefix}/settings/traffic.json` |

不导出：`GLOBAL`、`SSO`、`LDAP`、`S3`、`FTP`、`MEDIA`、`TRANSCODE`。
另外始终跳过 `token`、SSO/LDAP/S3 密钥、`ocr_api`、`version` 等键
（`version` 每次构建都会变，导出会制造无意义的提交）。

`include_storages=true` 时额外导出 `/{prefix}/storages.json`：

- 内容为所有已启用、且驱动不是 `Local` 的挂载（`mount_path`、`driver`、
  `order`、`addition`、`remark`、缓存/签名开关等）；
- **只导出，不导入**。避免远端快照覆盖或重建本机挂载；
- `addition` 里可能含各网盘 token，请确保仓库为私有库。

## 运行时行为

1. `bootstrap.Start()` 在 `EnsureLocalDriveStorages()` 之后、`LoadStorages()` 之前
   调用 `configsync.Boot(ctx)`：
   - 探测组类型（config-sync / data-sync），远端没有组时自动初始化；
   - config-sync：拉取 `/{prefix}` 到 `{data}/configsync/local/`；
   - data-sync：用本地 config 组登记数据组，直接读取远端文件，不落本地镜像；
   - 读取 `settings/*.json`，与当前 DB 值比较，只对有差异的键调用
     `op.SaveSettingItem`（保留 Type/Group/Flag）；
   - 基于当前设置重建快照并写回远端；
   - 注册 `RegisterSettingChangingCallback`，设置变更后 debounce 推送。
2. 应用设置时用 `applying` 原子标志抑制回调，避免「应用 → 触发推送」递归。

## 镜像语义

- 本地快照是唯一事实来源（在该 prefix 内），远端多余文件会被删除并写墓碑；
- 墓碑在 `.meta/tombstones/`，`deletedAt + 3000ms` 内重建的文件视为删除竞态的一部分；
- 不镜像 `.meta/tombstones`、`.meta/.deleted`、`.meta/conflicts`、`.meta/versions`、
  `*.mtime`、`.keep`；
- 冲突不做归档（JS 版的 `.meta/conflicts/` 未实现），按「本地胜出」处理，
  冲突文件不会保留副本。

## 已验证

- Go CLI 写入 → Gitee（`openlist-data` 的 `go-sync-test` 分支）→ 另一端 pull，
  4 个文件字节完全一致（SHA-256 相同）；
- JS `zen-fs-config` + `zen-fs-gitee` 直接读取 Go 写出的
  `/openlist/settings/site.json`（74 B）与 `.version`（152 B），
  `JSON.parse` 得到 `site_title="OpenList Test"`；
- JS 通过 `setConfig` + `flush` 写入 `settings/js-written.json`，Go pull 回来字节一致；
- 用临时数据目录启动 OpenList：日志显示
  `applied 1 settings under /openlist: site_title`、`exported 0 storages`、
  `synced weijia432/openlist-data branch=go-sync-test prefix=/openlist`。

## 排错

- `group type mismatch`：`group_type` 显式配置与远端标记不一致。改成 `auto`，
  或把配置改成远端实际的类型。
- `unknown setting "xxx"`：快照里出现了 DB 中不存在的键，通常是手工放进
  `settings/` 的测试文件，忽略即可。
- data-sync 组没有 `.meta/group.json` 且 `data_group` 为空：会自动创建
  `id=default` 的数据组；已有仓库请填写真实 id。
- 推送失败 401：token 错误或没有仓库写权限。
- 推送失败 404：Gitee 对不存在的路径返回 `200 []`，本库已按响应体形状处理；
  若仍出现 404，检查 `branch` 是否存在。
