// Package configsync syncs a whitelisted subset of OpenList settings (and a
// read-only storage snapshot) into a zen-fs-config compatible group, and
// applies remote settings at startup.
//
// It is disabled unless data/configsync.json exists (or OPENLIST_CONFIGSYNC
// points at one), so a normal OpenList install is unaffected.
package configsync

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
)

// Config is the on-disk configuration of the feature.
type Config struct {
	Enabled bool `json:"enabled"`
	Gitee   struct {
		Owner   string `json:"owner"`
		Repo    string `json:"repo"`
		Branch  string `json:"branch"`
		Token   string `json:"token"`
		BaseURL string `json:"base_url"`
	} `json:"gitee"`
	AppID           string `json:"app_id"`
	Prefix          string `json:"prefix"`
	IncludeStorages bool   `json:"include_storages"`
	// GroupType is "auto" (default), "config-sync" or "data-sync". In auto
	// mode the remote marker decides; a remote without any marker is treated
	// as config-sync, matching zen-fs-config's default.
	GroupType string `json:"group_type"`
	// DataGroup is the registered id of a data-sync group. Empty means "read
	// .meta/group.json", falling back to "default".
	DataGroup string `json:"data_group"`
	// DataGroupName is the display name used when a data group is registered.
	DataGroupName string `json:"data_group_name"`
	// AppData optionally registers a data group inside a config-sync group so
	// other devices can discover it from the config group alone.
	AppData *AppDataConfig `json:"app_data"`
	// PushDebounceSeconds controls how long to wait after a setting change
	// before pushing. Defaults to 5.
	PushDebounceSeconds int `json:"push_debounce_seconds"`
}

// AppDataConfig describes the app-data group advertised by a config-sync
// group. Repo points at a separate Gitee repository holding the data group.
type AppDataConfig struct {
	Enabled bool   `json:"enabled"`
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	GroupID string `json:"group_id"`
	Name    string `json:"name"`
}

func configPath() string {
	if p := os.Getenv("OPENLIST_CONFIGSYNC"); p != "" {
		return p
	}
	return filepath.Join(conf.DataDir, "configsync.json")
}

func loadConfig() (*Config, error) {
	path := configPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{Enabled: false}, nil
		}
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.AppID == "" {
		cfg.AppID = "openlist"
	}
	if cfg.Prefix == "" {
		cfg.Prefix = "/" + cfg.AppID
	}
	if cfg.PushDebounceSeconds <= 0 {
		cfg.PushDebounceSeconds = 5
	}
	if cfg.GroupType == "" {
		cfg.GroupType = "auto"
	}
	if cfg.DataGroupName == "" {
		cfg.DataGroupName = "OpenList"
	}
	if cfg.AppData != nil {
		if cfg.AppData.GroupID == "" {
			cfg.AppData.GroupID = "default"
		}
		if cfg.AppData.Name == "" {
			cfg.AppData.Name = "OpenList"
		}
		if cfg.AppData.Branch == "" {
			cfg.AppData.Branch = cfg.Gitee.Branch
		}
	}
	if cfg.Gitee.Token == "" {
		cfg.Gitee.Token = os.Getenv("GITEE_TOKEN")
	}
	return &cfg, nil
}

func localDir() string {
	return filepath.Join(conf.DataDir, "configsync", "local")
}
