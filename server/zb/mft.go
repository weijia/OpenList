package zb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/gin-gonic/gin"
)

const (
	maxScanDepth    = 24
	maxFilesPerDir  = 20000
	defaultScanTO   = 20 * time.Second
	scanConcurrency = 6
	hashConcurrency = 4
)

// 进程内缓存：同一路径 + size + mtime 的完整内容 SHA-256 不重复读。
// OpenList 的驱动不一定提供 SHA-256，所以缓存的是真正流式算出来的结果。
var (
	hashCacheMu sync.Mutex
	hashCache   = map[string]string{}
)

func hashCacheKey(path string, size, mtime int64) string {
	return fmt.Sprintf("%s\x00%d\x00%d", strings.ToLower(path), size, mtime)
}

func recallHash(key string) string {
	hashCacheMu.Lock()
	defer hashCacheMu.Unlock()
	return hashCache[key]
}

func rememberHash(key, value string) {
	hashCacheMu.Lock()
	defer hashCacheMu.Unlock()
	if len(hashCache) < 100000 {
		hashCache[key] = value
	}
}

type scanNode struct {
	Path            string     `json:"path"`
	Name            string     `json:"name"`
	Type            string     `json:"type"`
	Tags            []string   `json:"tags"`
	Mtime           int64      `json:"mtime,omitempty"`
	SizePartial     bool       `json:"sizePartial"`
	FileCount       int        `json:"fileCount"`
	TotalSize       int64      `json:"totalSize"`
	TotalSizeOnDisk int64      `json:"totalSizeOnDisk"`
	Children        []scanNode `json:"children"`
	files           []scanFile
	hashPartial     bool
}

type scanFile struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	SizeOnDisk int64  `json:"sizeOnDisk"`
	Mtime      int64  `json:"mtime"`
	Hash       string `json:"hash,omitempty"`
	HashError  string `json:"hashError,omitempty"`
	obj        model.Obj
	reqPath    string
}

type scanSummary struct {
	FileCount       int   `json:"fileCount"`
	TotalSize       int64 `json:"totalSize"`
	TotalSizeOnDisk int64 `json:"totalSizeOnDisk"`
	Partial         bool  `json:"partial"`
}

type scanOptions struct {
	recursive  bool
	sizeFull   bool
	maxDepth   int
	maxFiles   int
	deadline   time.Time
	hash       bool
	hashSem    chan struct{}
	hashHeader http.Header
}

func scanExpired(opts scanOptions) bool { return time.Now().After(opts.deadline) }

// hashVirtualFile 计算 OpenList 虚拟文件的完整内容 SHA-256。
// 优先用驱动自带的 SHA-256；没有时通过 fs.Link 的 RangeReader / URL 流式读取，
// 不假设文件在本机磁盘上，也不依赖驱动提供 SHA-256。
func hashVirtualFile(ctx context.Context, reqPath string, obj model.Obj, size, mtime int64, header http.Header) (string, error) {
	if obj != nil {
		if value := obj.GetHash().GetHash(utils.SHA256); value != "" {
			value = strings.ToLower(strings.TrimSpace(value))
			if len(value) == 64 {
				return "sha256:" + value, nil
			}
		}
	}
	cacheKey := hashCacheKey(reqPath, size, mtime)
	if value := recallHash(cacheKey); value != "" {
		return value, nil
	}

	link, _, err := fs.Link(ctx, reqPath, model.LinkArgs{Header: header})
	if err != nil {
		return "", err
	}
	defer link.Close()

	var reader io.ReadCloser
	if link.RangeReader != nil {
		reader, err = link.RangeReader.RangeRead(ctx, http_range.Range{Start: 0, Length: size})
	} else if link.URL != "" {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, link.URL, nil)
		if reqErr != nil {
			return "", reqErr
		}
		for key, values := range link.Header {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			return "", doErr
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			resp.Body.Close()
			return "", fmt.Errorf("读取文件内容失败: HTTP %d", resp.StatusCode)
		}
		reader = resp.Body
	} else {
		return "", fmt.Errorf("该驱动未提供可用的读取链接")
	}
	if err != nil {
		return "", err
	}
	defer reader.Close()

	sum := sha256.New()
	if _, err := io.Copy(sum, reader); err != nil {
		return "", err
	}
	value := "sha256:" + hex.EncodeToString(sum.Sum(nil))
	rememberHash(cacheKey, value)
	return value, nil
}

func scanDir(ctx context.Context, user *model.User, display string, reqPath string, depth int, opts scanOptions) scanNode {
	node := scanNode{
		Path:     display,
		Name:     baseName(display),
		Type:     "folder",
		Tags:     []string{},
		Children: []scanNode{},
		files:    []scanFile{},
	}
	if opts.maxDepth > 0 && depth > opts.maxDepth || scanExpired(opts) {
		node.SizePartial = true
		node.Tags = []string{}
		return node
	}
	childCtx, cancel := context.WithDeadline(ctx, opts.deadline)
	defer cancel()
	objs, err := fs.List(childCtx, reqPath, &fs.ListArgs{NoLog: true})
	if err != nil {
		node.SizePartial = true
		node.Tags = []string{}
		return node
	}
	names := make([]string, 0, len(objs))
	for _, obj := range objs {
		names = append(names, obj.GetName())
	}
	node.Tags = detectTags(names)

	dirs := make([]model.Obj, 0, len(objs))
	for _, obj := range objs {
		name := obj.GetName()
		if name == "" {
			continue
		}
		if obj.IsDir() {
			dirs = append(dirs, obj)
			continue
		}
		file := scanFile{
			Path:       joinVirtual(display, name),
			Name:       name,
			Size:       obj.GetSize(),
			SizeOnDisk: obj.GetSize(),
			Mtime:      obj.ModTime().UnixMilli(),
			obj:        obj,
			reqPath:    joinVirtual(reqPath, name),
		}
		node.files = append(node.files, file)
		node.FileCount++
		node.TotalSize += file.Size
		node.TotalSizeOnDisk += file.SizeOnDisk
	}
	if opts.hash && len(node.files) > 0 {
		hashes := make([]string, len(node.files))
		errs := make([]string, len(node.files))
		var wg sync.WaitGroup
		hashCtx, hashCancel := context.WithDeadline(ctx, opts.deadline)
		defer hashCancel()
		for index := range node.files {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				if scanExpired(opts) {
					errs[index] = "scan timeout"
					return
				}
				if opts.hashSem != nil {
					select {
					case opts.hashSem <- struct{}{}:
						defer func() { <-opts.hashSem }()
					case <-hashCtx.Done():
						errs[index] = "scan timeout"
						return
					}
				}
				file := node.files[index]
				value, err := hashVirtualFile(hashCtx, file.reqPath, file.obj, file.Size, file.Mtime, opts.hashHeader)
				if err != nil {
					if hashCtx.Err() != nil {
						errs[index] = "scan timeout"
					} else {
						text := err.Error()
						if len(text) > 160 {
							text = text[:160]
						}
						errs[index] = text
					}
					return
				}
				hashes[index] = value
			}(index)
		}
		wg.Wait()
		for index := range node.files {
			if hashes[index] != "" {
				node.files[index].Hash = hashes[index]
			}
			if errs[index] != "" {
				node.files[index].HashError = errs[index]
				node.hashPartial = true
			}
		}
	}
	if opts.recursive && len(dirs) > 0 && !scanExpired(opts) {
		sem := make(chan struct{}, scanConcurrency)
		var wg sync.WaitGroup
		var mu sync.Mutex
		results := make([]scanNode, len(dirs))
		for index, dir := range dirs {
			wg.Add(1)
			sem <- struct{}{}
			go func(index int, dir model.Obj) {
				defer wg.Done()
				defer func() { <-sem }()
				childDisplay := joinVirtual(display, dir.GetName())
				childReq := reqPath + "/" + dir.GetName()
				child := scanDir(ctx, user, childDisplay, childReq, depth+1, opts)
				results[index] = child
			}(index, dir)
		}
		wg.Wait()
		mu.Lock()
		for _, child := range results {
			node.Children = append(node.Children, child)
			node.FileCount += child.FileCount
			node.TotalSize += child.TotalSize
			node.TotalSizeOnDisk += child.TotalSizeOnDisk
			if child.SizePartial {
				node.SizePartial = true
			}
			if child.hashPartial {
				node.hashPartial = true
			}
		}
		mu.Unlock()
	}
	if len(node.Children) == 0 {
		node.Children = []scanNode{}
	}
	return node
}

type mftScanReq struct {
	Paths     []any `json:"paths"`
	Recursive *bool `json:"recursive"`
	SizeFull  *bool `json:"sizeFull"`
	Hash      *bool `json:"hash"`
	TimeoutMs int   `json:"timeoutMs"`
}

func apiMftScan(c *gin.Context, user *model.User) {
	var req mftScanReq
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	if len(req.Paths) > 100 {
		req.Paths = req.Paths[:100]
	}
	timeout := defaultScanTO
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
	}
	if timeout > 120*time.Second {
		timeout = 120 * time.Second
	}
	if timeout < time.Second {
		timeout = time.Second
	}
	deadline := time.Now().Add(timeout)

	recursive := true
	if req.Recursive != nil {
		recursive = *req.Recursive
	}
	sizeFull := false
	if req.SizeFull != nil {
		sizeFull = *req.SizeFull
	}
	hash := false
	if req.Hash != nil {
		hash = *req.Hash
	}
	var hashSem chan struct{}
	if hash {
		hashSem = make(chan struct{}, hashConcurrency)
	}

	roots := make([]scanNode, 0, len(req.Paths))
	summaries := map[string]scanSummary{}
	files := map[string][]scanFile{}
	status := map[string]gin.H{}

	for _, raw := range req.Paths {
		entryPath := ""
		entryRecursive := recursive
		entrySizeFull := sizeFull
		switch value := raw.(type) {
		case string:
			entryPath = value
		case map[string]any:
			if p, ok := value["path"].(string); ok {
				entryPath = p
			}
			if r, ok := value["recursive"].(bool); ok {
				entryRecursive = r
			}
			if s, ok := value["sizeFull"].(bool); ok {
				entrySizeFull = s
			}
		}
		p := normalizeVirtual(entryPath)
		if p == "/" && entryPath == "" {
			continue
		}
		reqPath, err := user.JoinPath(p)
		if err != nil {
			status[p] = gin.H{"incomplete": true, "truncated": false, "total": 0, "error": err.Error()}
			continue
		}
		node := scanDir(c.Request.Context(), user, p, reqPath, 0, scanOptions{
			recursive:  entryRecursive || entrySizeFull,
			sizeFull:   entrySizeFull,
			maxDepth:   maxScanDepth,
			maxFiles:   maxFilesPerDir,
			deadline:   deadline,
			hash:       hash,
			hashSem:    hashSem,
			hashHeader: c.Request.Header,
		})
		roots = append(roots, stripScanFiles(node, entryRecursive))
		collectScanSummaries(node, &summaries)
		gathered := make([]scanFile, 0, len(node.files))
		collectScanFiles(node, &gathered, 0, entryRecursive)
		if len(gathered) > maxFilesPerDir {
			gathered = gathered[:maxFilesPerDir]
		}
		files[p] = gathered
		status[p] = gin.H{
			"incomplete": node.SizePartial,
			"truncated":  len(gathered) >= maxFilesPerDir,
			"total":      len(gathered),
		}
		if hash {
			status[p]["hashPartial"] = node.hashPartial
		}
	}
	writeJSON(c, http.StatusOK, gin.H{
		"roots":     roots,
		"summaries": summaries,
		"files":     files,
		"status":    status,
	})
}

func stripScanFiles(node scanNode, recursive bool) scanNode {
	node.files = nil
	children := make([]scanNode, 0, len(node.Children))
	for _, child := range node.Children {
		if !recursive {
			child.Children = []scanNode{}
			child.files = nil
		} else {
			child = stripScanFiles(child, true)
		}
		children = append(children, child)
	}
	node.Children = children
	return node
}

func collectScanSummaries(node scanNode, out *map[string]scanSummary) {
	(*out)[node.Path] = scanSummary{
		FileCount:       node.FileCount,
		TotalSize:       node.TotalSize,
		TotalSizeOnDisk: node.TotalSizeOnDisk,
		Partial:         node.SizePartial,
	}
	for _, child := range node.Children {
		collectScanSummaries(child, out)
	}
}

func collectScanFiles(node scanNode, out *[]scanFile, depth int, recursive bool) {
	*out = append(*out, node.files...)
	if !recursive {
		return
	}
	for _, child := range node.Children {
		collectScanFiles(child, out, depth+1, true)
	}
}

func apiMftTree(c *gin.Context, user *model.User) {
	p := normalizeVirtual(c.Query("path"))
	reqPath, err := user.JoinPath(p)
	if err != nil {
		writeError(c, http.StatusForbidden, err.Error())
		return
	}
	objs, err := fs.List(c.Request.Context(), reqPath, &fs.ListArgs{NoLog: true})
	if err != nil {
		writeError(c, http.StatusBadRequest, "无法读取该目录: "+err.Error())
		return
	}
	names := make([]string, 0, len(objs))
	children := make([]gin.H, 0, len(objs))
	for _, obj := range objs {
		name := obj.GetName()
		names = append(names, name)
		if !obj.IsDir() {
			continue
		}
		children = append(children, gin.H{"name": name, "path": joinVirtual(p, name)})
	}
	probeTreeChildren(c.Request.Context(), user, children)
	writeJSON(c, http.StatusOK, gin.H{
		"path":     p,
		"name":     baseName(p),
		"tags":     detectTags(names),
		"children": children,
	})
}

// probeTreeChildren 给每个直接子目录探测“是否还有下一层”。
// 返回 false 时前端可以不再画展开箭头；读不到时保持 null，按未知处理，避免误标。
func probeTreeChildren(ctx context.Context, user *model.User, children []gin.H) {
	const probeConcurrency = 6
	sem := make(chan struct{}, probeConcurrency)
	var wg sync.WaitGroup
	for i := range children {
		childPath, _ := children[i]["path"].(string)
		wg.Add(1)
		go func(i int, path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			children[i]["hasChildren"] = probeHasChildDirs(ctx, user, path)
		}(i, childPath)
	}
	wg.Wait()
}

func probeHasChildDirs(ctx context.Context, user *model.User, virtualPath string) *bool {
	reqPath, err := user.JoinPath(virtualPath)
	if err != nil {
		return nil
	}
	objs, err := fs.List(ctx, reqPath, &fs.ListArgs{NoLog: true})
	if err != nil {
		return nil
	}
	for _, obj := range objs {
		if obj.IsDir() {
			has := true
			return &has
		}
	}
	has := false
	return &has
}
