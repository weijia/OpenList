package configsync

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	zc "github.com/weijia/go-zenfs-config"
	"github.com/weijia/go-zenfs-config/backend/folder"

	log "github.com/sirupsen/logrus"
)

// settingGroups is the default export scope: presentation and behaviour
// settings that are safe to share, excluding credentials (GLOBAL/SSO/LDAP/
// S3/FTP/MEDIA/TRANSCODE).
var settingGroups = map[int]string{
	model.SITE:    "site",
	model.STYLE:   "style",
	model.PREVIEW: "preview",
	model.INDEX:   "index",
	model.TRAFFIC: "traffic",
}

// sensitiveKeys are never exported even if they live in a whitelisted group.
// sensitiveKeys are never exported even if they live in a whitelisted group.
// "version" is build metadata that changes on every upgrade, so exporting it
// would create an endless stream of commits.
var sensitiveKeys = map[string]bool{
	"token": true, "sso_client_secret": true, "ldap_manager_password": true,
	"s3_secret_access_key": true, "ocr_api": true, "transcode_worker_secret": true,
	"sso_jwt_public_key": true, "sso_client_id": true, "version": true,
}

// BuildSnapshot writes the current settings (and optionally a storage
// snapshot) into local under /{appId}/.
func BuildSnapshot(ctx context.Context, local *folder.Backend, cfg *Config) error {
	repo, err := zc.Open(ctx, local, cfg.AppID)
	if err != nil {
		return err
	}
	repo.Author = "openlist/" + cfg.AppID
	items, err := op.GetSettingItems()
	if err != nil {
		return err
	}
	for group, name := range settingGroups {
		values := map[string]any{}
		for _, it := range items {
			if it.Group != group || it.IsDeprecated() || sensitiveKeys[it.Key] {
				continue
			}
			values[it.Key] = it.Value
		}
		if len(values) == 0 {
			continue
		}
		if _, err := repo.SetConfig(ctx, "settings/"+name, values); err != nil {
			return err
		}
	}
	if cfg.IncludeStorages {
		if err := writeStorages(ctx, repo); err != nil {
			return err
		}
	}
	return nil
}

func writeStorages(ctx context.Context, repo *zc.Repo) error {
	out, err := storageSnapshot()
	if err != nil {
		return err
	}
	if _, err := repo.SetConfig(ctx, "storages", out); err != nil {
		return err
	}
	log.Infof("[configsync] exported %d storages", len(out))
	return nil
}

// BuildDataSnapshot writes the same payload into a data-sync group. Files are
// opaque to zen-fs-config, so plain indented JSON is enough; map keys are
// sorted by encoding/json, which keeps the bytes stable between pushes.
func BuildDataSnapshot(ctx context.Context, d *zc.DataGroup, cfg *Config) error {
	items, err := op.GetSettingItems()
	if err != nil {
		return err
	}
	for group, name := range settingGroups {
		values := map[string]any{}
		for _, it := range items {
			if it.Group != group || it.IsDeprecated() || sensitiveKeys[it.Key] {
				continue
			}
			values[it.Key] = it.Value
		}
		if len(values) == 0 {
			continue
		}
		data, err := json.MarshalIndent(values, "", "  ")
		if err != nil {
			return err
		}
		if _, err := d.WriteFile(ctx, settingsPath(cfg.Prefix, name), data); err != nil {
			return err
		}
	}
	if cfg.IncludeStorages {
		out, err := storageSnapshot()
		if err != nil {
			return err
		}
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		if _, err := d.WriteFile(ctx, strings.TrimSuffix(cfg.Prefix, "/")+"/storages.json", data); err != nil {
			return err
		}
		log.Infof("[configsync] exported %d storages", len(out))
	}
	return nil
}

func settingsPath(prefix, name string) string {
	return strings.TrimSuffix(prefix, "/") + "/settings/" + name + ".json"
}

func storageSnapshot() ([]any, error) {
	storages, err := db.GetEnabledStorages()
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(storages))
	for _, s := range storages {
		if s.Driver == "Local" {
			continue // local mounts are created automatically on every start
		}
		entry := map[string]any{
			"mount_path":       s.MountPath,
			"driver":           s.Driver,
			"order":            s.Order,
			"remark":           s.Remark,
			"disabled":         s.Disabled,
			"disable_index":    s.DisableIndex,
			"enable_sign":      s.EnableSign,
			"cache_expiration": s.CacheExpiration,
		}
		if s.Addition != "" {
			var addition any
			if err := json.Unmarshal([]byte(s.Addition), &addition); err == nil {
				entry["addition"] = addition
			} else {
				entry["addition_raw"] = s.Addition
			}
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		a := out[i].(map[string]any)["mount_path"].(string)
		b := out[j].(map[string]any)["mount_path"].(string)
		return a < b
	})
	return out, nil
}
