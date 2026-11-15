package zb

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/gin-gonic/gin"
)

const (
	maxScanDepth    = 24
	maxFilesPerDir  = 20000
	defaultScanTO   = 20 * time.Second
	scanConcurrency = 6
)

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
}

type scanFile struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	SizeOnDisk int64  `json:"sizeOnDisk"`
	Mtime      int64  `json:"mtime"`
}

type scanSummary struct {
	FileCount       int   `json:"fileCount"`
	TotalSize       int64 `json:"totalSize"`
	TotalSizeOnDisk int64 `json:"totalSizeOnDisk"`
	Partial         bool  `json:"partial"`
}

type scanOptions struct {
	recursive bool
	sizeFull  bool
	maxDepth  int
	maxFiles  int
	deadline  time.Time
}

func scanExpired(opts scanOptions) bool { return time.Now().After(opts.deadline) }

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
		}
		node.files = append(node.files, file)
		node.FileCount++
		node.TotalSize += file.Size
		node.TotalSizeOnDisk += file.SizeOnDisk
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
			recursive: entryRecursive || entrySizeFull,
			sizeFull:  entrySizeFull,
			maxDepth:  maxScanDepth,
			maxFiles:  maxFilesPerDir,
			deadline:  deadline,
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
	writeJSON(c, http.StatusOK, gin.H{
		"path":     p,
		"name":     baseName(p),
		"tags":     detectTags(names),
		"children": children,
	})
}
