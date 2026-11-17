package configsync

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/op"
	zc "github.com/weijia/go-zenfs-config"
	"github.com/weijia/go-zenfs-config/backend/folder"
	"github.com/weijia/go-zenfs-config/backend/gitee"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

var (
	cfg      *Config
	local    *folder.Backend
	remote   *gitee.Backend
	data     *zc.DataGroup
	applying atomic.Bool
	pushMu   sync.Mutex
	armed    atomic.Bool
)

// Boot pulls the remote group, applies settings, exports a fresh snapshot and
// pushes it back. It must run after Init() (DB ready) and before
// LoadStorages(), because applying settings changes what the server loads.
//
// The group type is detected from the remote marker: a config-sync group uses
// the mirror + /{prefix}/settings flow, a data-sync group is driven directly
// through its DataGroup API. A missing or disabled config makes Boot a no-op.
func Boot(ctx context.Context) {
	c, err := loadConfig()
	if err != nil {
		log.Warnf("[configsync] config: %v", err)
		return
	}
	if !c.Enabled {
		return
	}
	if c.Gitee.Owner == "" || c.Gitee.Repo == "" {
		log.Warn("[configsync] enabled but gitee.owner/repo missing")
		return
	}
	cfg = c

	remote, err = gitee.New(gitee.Config{
		Owner: c.Gitee.Owner, Repo: c.Gitee.Repo, Branch: c.Gitee.Branch,
		Token: c.Gitee.Token, BaseURL: c.Gitee.BaseURL, Message: "openlist configsync",
	})
	if err != nil {
		log.Errorf("[configsync] gitee backend: %v", err)
		return
	}
	local, err = folder.New(localDir())
	if err != nil {
		log.Errorf("[configsync] local dir: %v", err)
		return
	}

	groupType, err := resolveGroupType(ctx, c)
	if err != nil {
		log.Errorf("[configsync] group type: %v", err)
		return
	}
	log.Infof("[configsync] remote %s/%s detected as %s", c.Gitee.Owner, c.Gitee.Repo, groupType)

	if groupType == zc.GroupTypeData {
		if err := bootData(ctx, c); err != nil {
			log.Errorf("[configsync] data group: %v", err)
			return
		}
	} else {
		if err := bootConfig(ctx, c); err != nil {
			log.Errorf("[configsync] config group: %v", err)
			return
		}
	}

	if armed.CompareAndSwap(false, true) {
		op.RegisterSettingChangingCallback(schedulePush)
	}
}

// resolveGroupType applies the auto/config-sync/data-sync contract. An explicit
// configuration that disagrees with the remote marker is rejected instead of
// silently writing into the wrong kind of group.
func resolveGroupType(ctx context.Context, c *Config) (string, error) {
	detected, found, err := zc.DetectGroupType(ctx, remote)
	if err != nil {
		return "", err
	}
	if found {
		if c.GroupType != "" && c.GroupType != "auto" && c.GroupType != detected {
			return "", fmt.Errorf("%w: configured %s, remote is %s", zc.ErrGroupTypeMismatch, c.GroupType, detected)
		}
		return detected, nil
	}
	if c.GroupType != "" && c.GroupType != "auto" {
		return c.GroupType, nil
	}
	// No marker. A brand-new remote is initialised as a config group, which is
	// also the zen-fs-config default for an unmarked group.
	if _, _, err := zc.ReadGroupDescriptor(ctx, remote); err != nil {
		if !errors.Is(err, zc.ErrNotExist) {
			return "", err
		}
		if _, err := zc.EnsureGroup(ctx, remote, zc.NewConfigGroup("config", "")); err != nil {
			return "", err
		}
		log.Infof("[configsync] initialised empty remote as a config-sync group")
	}
	return zc.GroupTypeConfig, nil
}

func bootConfig(ctx context.Context, c *Config) error {
	if _, err := zc.Pull(ctx, remote, local, zc.MirrorOptions{Prefixes: []string{c.Prefix}, Author: "openlist"}); err != nil {
		return errors.WithMessage(err, "pull")
	}
	if err := applySettings(ctx, c, local); err != nil {
		log.Errorf("[configsync] apply: %v", err)
	}
	if err := BuildSnapshot(ctx, local, c); err != nil {
		log.Errorf("[configsync] snapshot: %v", err)
	} else if _, err := zc.Push(ctx, local, remote, zc.MirrorOptions{Prefixes: []string{c.Prefix}, Author: "openlist"}); err != nil {
		return errors.WithMessage(err, "push")
	}

	if err := ensureAppDataGroup(ctx, c); err != nil {
		log.Warnf("[configsync] app-data group: %v", err)
	}
	log.Infof("[configsync] synced %s/%s branch=%s prefix=%s group=config-sync",
		c.Gitee.Owner, c.Gitee.Repo, c.Gitee.Branch, c.Prefix)
	return nil
}

// bootData drives a data-sync group directly: settings are read from the data
// group and written back through its versioned file API. No mirror copy is
// needed, so a large data group never lands in the OpenList data directory.
func bootData(ctx context.Context, c *Config) error {
	id := c.DataGroup
	if id == "" {
		if _, g, err := zc.ReadGroupDescriptor(ctx, remote); err == nil {
			id = g.ID
		}
	}
	if id == "" {
		id = "default"
	}
	be := giteeBackendDescriptor(c)
	// Reuse an existing backend descriptor that already points at this remote,
	// so booting OpenList does not add a duplicate gitee backend to the group.
	if dg, err := zc.OpenDataGroup(ctx, remote, c.AppID, id); err == nil {
		for _, existing := range dg.Backends() {
			if sameRemoteBackend(existing, be) {
				be.ID = existing.ID
				break
			}
		}
	}
	res, err := zc.Connect(ctx, zc.ConnectOptions{
		AppID:         c.AppID,
		NodeID:        "openlist",
		Storage:       remote,
		DataStorage:   remote,
		ConfigStorage: local,
		GroupType:     zc.GroupTypeData,
		DataGroupID:   id,
		DataGroupName: c.DataGroupName,
		Backend:       &be,
		Author:        "openlist",
	})
	if err != nil {
		return err
	}
	data = res.Data
	if err := applySettingsFromData(ctx, c, data); err != nil {
		log.Errorf("[configsync] apply: %v", err)
	}
	if err := BuildDataSnapshot(ctx, data, c); err != nil {
		return errors.WithMessage(err, "snapshot")
	}
	log.Infof("[configsync] synced %s/%s branch=%s prefix=%s group=data-sync id=%s",
		c.Gitee.Owner, c.Gitee.Repo, c.Gitee.Branch, c.Prefix, id)
	return nil
}

func giteeBackendDescriptor(c *Config) zc.BackendDescriptor {
	opts := map[string]any{
		"owner":  c.Gitee.Owner,
		"repo":   c.Gitee.Repo,
		"branch": c.Gitee.Branch,
	}
	if c.Gitee.BaseURL != "" {
		opts["baseUrl"] = c.Gitee.BaseURL
	}
	return zc.BackendDescriptor{ID: "gitee-primary", Type: "gitee", Options: opts}
}

// sameRemoteBackend reports whether two descriptors address the same remote.
func sameRemoteBackend(a, b zc.BackendDescriptor) bool {
	if a.Type != b.Type {
		return false
	}
	for _, k := range []string{"owner", "repo", "branch", "baseUrl"} {
		if fmt.Sprint(a.Options[k]) != fmt.Sprint(b.Options[k]) {
			return false
		}
	}
	return true
}

// ensureAppDataGroup advertises a data group from a config-sync group. The
// bootstrap descriptor lives in the config group; the data group's own
// .meta/backends stays authoritative.
func ensureAppDataGroup(ctx context.Context, c *Config) error {
	if c.AppData == nil || !c.AppData.Enabled || c.AppData.Repo == "" {
		return nil
	}
	store, err := gitee.New(gitee.Config{
		Owner: c.Gitee.Owner, Repo: c.AppData.Repo, Branch: c.AppData.Branch,
		Token: c.Gitee.Token, BaseURL: c.Gitee.BaseURL, Message: "openlist appdata",
	})
	if err != nil {
		return err
	}
	repo := &zc.Repo{Storage: remote, AppID: c.AppID, Author: "openlist"}
	be := zc.BackendDescriptor{ID: "gitee-primary", Type: "gitee", Options: map[string]any{
		"owner":  c.Gitee.Owner,
		"repo":   c.AppData.Repo,
		"branch": c.AppData.Branch,
	}}
	if c.Gitee.BaseURL != "" {
		be.Options["baseUrl"] = c.Gitee.BaseURL
	}
	name := c.AppData.Name
	if name == "" {
		name = c.DataGroupName
	}
	if err := repo.EnsureAppDataGroup(ctx, c.AppData.GroupID, store, name, &be); err != nil {
		return err
	}
	log.Infof("[configsync] app-data group %s -> %s/%s", c.AppData.GroupID, c.Gitee.Owner, c.AppData.Repo)
	return nil
}

func schedulePush() {
	if applying.Load() || cfg == nil {
		return
	}
	time.AfterFunc(time.Duration(cfg.PushDebounceSeconds)*time.Second, func() {
		if err := pushNow(context.Background()); err != nil {
			log.Warnf("[configsync] push: %v", err)
		}
	})
}

func pushNow(ctx context.Context) error {
	pushMu.Lock()
	defer pushMu.Unlock()
	if cfg == nil || remote == nil {
		return nil
	}
	if data != nil {
		return BuildDataSnapshot(ctx, data, cfg)
	}
	if local == nil {
		return nil
	}
	if err := BuildSnapshot(ctx, local, cfg); err != nil {
		return err
	}
	_, err := zc.Push(ctx, local, remote, zc.MirrorOptions{Prefixes: []string{cfg.Prefix}, Author: "openlist"})
	return err
}

// applySettings reads /{prefix}/settings/*.json from the local mirror and
// writes changed values through op.SaveSettingItem, which keeps Type, Group
// and flags of existing items intact. Unknown keys are logged and skipped.
func applySettings(ctx context.Context, c *Config, l *folder.Backend) error {
	files, err := zc.Walk(ctx, l, "/")
	if err != nil {
		return err
	}
	return applySettingValues(c, func(f zc.Entry) ([]byte, error) {
		return l.ReadFile(ctx, f.Path)
	}, files)
}

// applySettingsFromData is the data-sync counterpart of applySettings.
func applySettingsFromData(ctx context.Context, c *Config, d *zc.DataGroup) error {
	files, err := d.Walk(ctx)
	if err != nil {
		return err
	}
	return applySettingValues(c, func(f zc.Entry) ([]byte, error) {
		return d.ReadFile(ctx, f.Path)
	}, files)
}

func applySettingValues(c *Config, read func(zc.Entry) ([]byte, error), files []zc.Entry) error {
	current := op.GetSettingsMap()
	type update struct{ key, value string }
	var updates []update
	for _, f := range files {
		if !strings.HasPrefix(f.Path, c.Prefix+"/settings/") || !strings.HasSuffix(f.Path, ".json") {
			continue
		}
		data, err := read(f)
		if err != nil {
			return err
		}
		var values map[string]any
		if err := json.Unmarshal(data, &values); err != nil {
			log.Warnf("[configsync] skip %s: %v", f.Path, err)
			continue
		}
		for k, v := range values {
			val := fmt.Sprint(v)
			if current[k] != val {
				updates = append(updates, update{k, val})
			}
		}
	}
	if len(updates) == 0 {
		return nil
	}
	applying.Store(true)
	defer applying.Store(false)
	var changed []string
	for _, u := range updates {
		item, err := op.GetSettingItemByKey(u.key)
		if err != nil {
			log.Warnf("[configsync] unknown setting %q: %v", u.key, err)
			continue
		}
		item.Value = u.value
		if err := op.SaveSettingItem(item); err != nil {
			return errors.WithMessagef(err, "save %s", u.key)
		}
		changed = append(changed, u.key)
	}
	if len(changed) > 0 {
		log.Infof("[configsync] applied %d settings under %s: %s", len(changed), c.Prefix, strings.Join(changed, ", "))
	}
	return nil
}
