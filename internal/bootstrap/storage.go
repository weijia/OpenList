package bootstrap

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/cmd/flags"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

const (
	autoStorageRemark       = "自动挂载本地磁盘"
	legacyAutoStorageRemark = "首次启动自动创建的本地目录"
	// autoMountMarkerV2 adds Android shared storage to installations that
	// already ran the first version of auto provisioning.
	autoMountMarkerV2 = "auto-mount-v2.done"
)

// EnsureLocalDriveStorages mounts every local drive root (C:\, D:\ ... on
// Windows, / and /sdcard on Android, / on other Unix) as a separate Local
// storage on first start, so the browser front-end opens with the real disks
// instead of an empty list.
// It only acts while every storage in the database is one of its own: once
// the user adds or edits a mount by hand, it leaves the configuration alone.
func EnsureLocalDriveStorages() {
	marker := filepath.Join(flags.DataDir, autoMountMarkerV2)
	if _, err := os.Stat(marker); err == nil {
		// already provisioned once; never touch the user's mounts again
		return
	}
	storages, _, err := db.GetStorages(1, 200)
	if err != nil {
		utils.Log.Errorf("failed check storages for auto mount: %+v", err)
		return
	}
	for _, s := range storages {
		if !isAutoStorage(s) {
			return
		}
	}
	roots := localDriveRoots()
	existing := make(map[string]bool, len(storages))
	for _, s := range storages {
		root := additionRoot(s.Addition)
		if root == "" {
			continue
		}
		key := normalizeRoot(root)
		if !containsRoot(roots, key) {
			// stale auto mount (e.g. the old <program>/files folder)
			if err := db.DeleteStorageById(s.ID); err != nil {
				utils.Log.Errorf("failed remove stale auto storage [%s]: %+v", s.MountPath, err)
			} else {
				utils.Log.Infof("removed stale auto storage [%s] -> %s", s.MountPath, root)
			}
			continue
		}
		existing[key] = true
	}
	for _, root := range roots {
		if existing[normalizeRoot(root)] {
			continue
		}
		mount := driveMountPath(root)
		if _, err := db.GetStorageByMountPath(mount); err == nil {
			continue
		}
		addition, err := json.Marshal(map[string]any{
			"root_folder_path": root,
			"thumbnail":        true,
			"show_hidden":      false,
			"mkdir_perm":       "777",
		})
		if err != nil {
			utils.Log.Errorf("failed encode auto storage addition for %s: %+v", root, err)
			continue
		}
		storage := model.Storage{
			MountPath: mount,
			Order:     0,
			Driver:    "Local",
			Addition:  string(addition),
			Remark:    autoStorageRemark,
			Modified:  time.Now(),
		}
		// Persist only; LoadStorages (called right after) instantiates the driver,
		// so each mount is registered exactly once.
		if err := db.CreateStorage(&storage); err != nil {
			utils.Log.Errorf("failed create auto storage for %s: %+v", root, err)
			continue
		}
		utils.Log.Infof("created auto storage [%s] -> %s (id=%d)", storage.MountPath, root, storage.ID)
	}
	// One-shot provisioning: from now on the mount list belongs to the user.
	if err := os.WriteFile(marker, []byte(time.Now().Format(time.RFC3339)+"\n"), 0o666); err != nil {
		utils.Log.Errorf("failed write auto mount marker: %+v", err)
	} else {
		utils.Log.Infof("auto mount finished, marker: %s", marker)
	}
}

// localDriveRoots lists existing drive roots.
func localDriveRoots() []string {
	if runtime.GOOS == "android" {
		// Android apps do not share the Unix root's view of storage. Expose the
		// device's shared storage as a separate mount when it is reachable
		// (MANAGE_EXTERNAL_STORAGE is required to see user files on Android 11+).
		roots := []string{"/"}
		if fi, err := os.Stat("/sdcard"); err == nil && fi.IsDir() {
			roots = append(roots, "/sdcard")
		}
		return roots
	}
	if runtime.GOOS != "windows" {
		return []string{"/"}
	}
	roots := make([]string, 0, 8)
	for c := 'A'; c <= 'Z'; c++ {
		root := string(c) + `:\`
		if fi, err := os.Stat(root); err == nil && fi.IsDir() {
			roots = append(roots, root)
		}
	}
	return roots
}

// driveMountPath maps C:\ to /C; Unix root becomes /local. On Android the
// shared storage root gets its familiar /sdcard mount path.
func driveMountPath(root string) string {
	if runtime.GOOS == "android" {
		if filepath.Clean(root) == "/sdcard" {
			return "/sdcard"
		}
		return "/local"
	}
	if runtime.GOOS != "windows" {
		return "/local"
	}
	letter := strings.TrimSuffix(strings.ToUpper(root), `:\`)
	return "/" + letter
}

// isAutoStorage also recognizes the first version of the auto mount, which
// pointed at the program's files folder, so it can be migrated away.
func isAutoStorage(s model.Storage) bool {
	return s.Remark == autoStorageRemark || s.Remark == legacyAutoStorageRemark
}

func additionRoot(addition string) string {
	var parsed struct {
		RootFolderPath string `json:"root_folder_path"`
	}
	if err := json.Unmarshal([]byte(addition), &parsed); err != nil {
		return ""
	}
	return parsed.RootFolderPath
}

func normalizeRoot(root string) string {
	clean := filepath.Clean(root)
	if runtime.GOOS == "windows" {
		clean = strings.ToUpper(clean)
	}
	return clean
}

func containsRoot(roots []string, key string) bool {
	for _, root := range roots {
		if normalizeRoot(root) == key {
			return true
		}
	}
	return false
}

func LoadStorages() {
	storages, err := db.GetEnabledStorages()
	if err != nil {
		utils.Log.Fatalf("failed get enabled storages: %+v", err)
	}
	go func(storages []model.Storage) {
		for i := range storages {
			err := op.LoadStorage(context.Background(), storages[i])
			if err != nil {
				utils.Log.Errorf("failed get enabled storages: %+v", err)
			} else {
				utils.Log.Infof("success load storage: [%s], driver: [%s], order: [%d]",
					storages[i].MountPath, storages[i].Driver, storages[i].Order)
			}
		}
		conf.SendStoragesLoadedSignal()
	}(storages)
}
