package remotestorage

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// RemoteStorage mounts a remote storage server speaking the remoteStorage
// protocol (https://remotestorage.io): documents are plain resources under
// <endpoint>/<path>, folders are listed with Accept: application/ld+json.
type RemoteStorage struct {
	model.Storage
	Addition
	client *http.Client
}

func (d *RemoteStorage) Config() driver.Config {
	return config
}

func (d *RemoteStorage) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *RemoteStorage) Init(ctx context.Context) error {
	d.Endpoint = strings.TrimRight(strings.TrimSpace(d.Endpoint), "/")
	if d.Endpoint == "" {
		return fmt.Errorf("endpoint is required")
	}
	d.Token = strings.TrimSpace(d.Token)
	if d.Token == "" {
		return fmt.Errorf("token is required")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if d.TlsInsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	d.client = &http.Client{Transport: transport, Timeout: 10 * time.Minute}
	// Probe the root folder so a bad token fails at init instead of at first browse.
	resp, err := d.do(ctx, http.MethodGet, d.dirURL(d.GetRootPath()), nil, map[string]string{
		"Accept": folderAccept,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("remote storage rejected the token (HTTP %d)", resp.StatusCode)
	case http.StatusNotFound:
		// Root folder has no listing yet; the mount is still usable for new files.
		return nil
	default:
		return fmt.Errorf("remote storage returned HTTP %d on init", resp.StatusCode)
	}
}

func (d *RemoteStorage) Drop(ctx context.Context) error {
	if d.client != nil {
		d.client.CloseIdleConnections()
	}
	return nil
}

func (d *RemoteStorage) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	target := d.dirURL(dir.GetPath())
	resp, err := d.do(ctx, http.MethodGet, target, nil, map[string]string{"Accept": folderAccept})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, errs.ObjectNotFound
	default:
		return nil, fmt.Errorf("list %s failed: HTTP %d", target, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var listing folderListing
	if err := json.Unmarshal(body, &listing); err != nil {
		return nil, fmt.Errorf("list %s is not a remoteStorage folder listing: %w", target, err)
	}
	objects := make([]model.Obj, 0, len(listing.Items))
	for key, item := range listing.Items {
		isFolder := strings.HasSuffix(key, "/") || strings.HasPrefix(item.ContentType, folderContentType)
		name := strings.TrimSuffix(key, "/")
		name = path.Base(name)
		if name == "" || name == "." || name == ".." {
			continue
		}
		objects = append(objects, &model.Object{
			Path:     path.Join(dir.GetPath(), name),
			Name:     name,
			Size:     item.ContentLength,
			IsFolder: isFolder,
		})
	}
	return objects, nil
}

func (d *RemoteStorage) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	return &model.Link{
		URL:    d.urlOf(file.GetPath()),
		Header: http.Header{"Authorization": {"Bearer " + d.Token}},
	}, nil
}

func (d *RemoteStorage) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	target := d.dirURL(path.Join(parentDir.GetPath(), dirName))
	resp, err := d.do(ctx, http.MethodPut, target, http.NoBody, map[string]string{
		"Content-Type": folderContentType,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// remoteStorage folders are implicit; some servers answer 409 for an existing one.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 || resp.StatusCode == http.StatusConflict {
		return nil
	}
	return fmt.Errorf("mkdir %s failed: HTTP %d", target, resp.StatusCode)
}

func (d *RemoteStorage) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	if err := d.copyObject(ctx, srcObj, path.Join(dstDir.GetPath(), srcObj.GetName())); err != nil {
		return err
	}
	return d.Remove(ctx, srcObj)
}

func (d *RemoteStorage) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	if err := d.copyObject(ctx, srcObj, path.Join(path.Dir(srcObj.GetPath()), newName)); err != nil {
		return err
	}
	return d.Remove(ctx, srcObj)
}

func (d *RemoteStorage) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	return d.copyObject(ctx, srcObj, path.Join(dstDir.GetPath(), srcObj.GetName()))
}

func (d *RemoteStorage) Remove(ctx context.Context, obj model.Obj) error {
	target := d.urlOf(obj.GetPath())
	if obj.IsDir() {
		target = d.dirURL(obj.GetPath())
	}
	resp, err := d.do(ctx, http.MethodDelete, target, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errs.ObjectNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("delete %s failed: HTTP %d", target, resp.StatusCode)
	}
	return nil
}

func (d *RemoteStorage) Put(ctx context.Context, dstDir model.Obj, s model.FileStreamer, up driver.UpdateProgress) error {
	target := d.urlOf(path.Join(dstDir.GetPath(), s.GetName()))
	reader := driver.NewLimitedUploadStream(ctx, &driver.ReaderUpdatingProgress{
		Reader:         s,
		UpdateProgress: up,
	})
	resp, err := d.upload(ctx, target, s.GetMimetype(), reader, s.GetSize())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("put %s failed: HTTP %d", target, resp.StatusCode)
	}
	return nil
}

// implements driver.Getter interface
func (d *RemoteStorage) Get(ctx context.Context, _path string) (model.Obj, error) {
	clean := strings.Trim(path.Clean("/"+_path), "/")
	if clean == "" || clean == "." {
		return &model.Object{Path: d.GetRootPath(), Name: path.Base(d.GetRootPath()), IsFolder: true}, nil
	}
	parent := path.Join(d.GetRootPath(), path.Dir(clean))
	objects, err := d.List(ctx, &model.Object{Path: parent, Name: path.Base(parent), IsFolder: true}, model.ListArgs{})
	if err != nil {
		return nil, err
	}
	base := path.Base(clean)
	for _, obj := range objects {
		if obj.GetName() == base {
			return obj, nil
		}
	}
	return nil, errs.ObjectNotFound
}

func (d *RemoteStorage) copyObject(ctx context.Context, srcObj model.Obj, dstPath string) error {
	if srcObj.IsDir() {
		if err := d.MakeDir(ctx, &model.Object{Path: path.Dir(dstPath), Name: path.Base(path.Dir(dstPath)), IsFolder: true}, path.Base(dstPath)); err != nil {
			return err
		}
		children, err := d.List(ctx, srcObj, model.ListArgs{})
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := d.copyObject(ctx, child, path.Join(dstPath, child.GetName())); err != nil {
				return err
			}
		}
		return nil
	}
	// Stream the source document straight into the destination document.
	srcURL := d.urlOf(srcObj.GetPath())
	resp, err := d.do(ctx, http.MethodGet, srcURL, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("read %s failed: HTTP %d", srcURL, resp.StatusCode)
	}
	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = "application/octet-stream"
	}
	dstResp, err := d.upload(ctx, d.urlOf(dstPath), mime, resp.Body, resp.ContentLength)
	if err != nil {
		return err
	}
	defer dstResp.Body.Close()
	if dstResp.StatusCode < 200 || dstResp.StatusCode >= 300 {
		return fmt.Errorf("copy to %s failed: HTTP %d", dstPath, dstResp.StatusCode)
	}
	return nil
}

// upload PUTs a document, using the remoteStorage preconditions: If-None-Match
// for a new document, If-Match with the current ETag for an overwrite.
func (d *RemoteStorage) upload(ctx context.Context, target, mime string, body io.Reader, size int64) (*http.Response, error) {
	headers := map[string]string{"Content-Type": mime}
	etag, exists, err := d.head(ctx, target)
	if err != nil {
		return nil, err
	}
	if exists {
		headers["If-Match"] = etag
	} else {
		headers["If-None-Match"] = "*"
	}
	req, err := d.newRequest(ctx, http.MethodPut, target, body, headers)
	if err != nil {
		return nil, err
	}
	if size >= 0 {
		req.ContentLength = size
	}
	return d.client.Do(req)
}

func (d *RemoteStorage) head(ctx context.Context, target string) (string, bool, error) {
	resp, err := d.do(ctx, http.MethodHead, target, nil, nil)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return resp.Header.Get("ETag"), true, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return "", false, nil
	}
	if resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented {
		return "", false, nil
	}
	return "", false, nil
}

func (d *RemoteStorage) do(ctx context.Context, method, target string, body io.Reader, headers map[string]string) (*http.Response, error) {
	req, err := d.newRequest(ctx, method, target, body, headers)
	if err != nil {
		return nil, err
	}
	return d.client.Do(req)
}

func (d *RemoteStorage) newRequest(ctx context.Context, method, target string, body io.Reader, headers map[string]string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+d.Token)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return req, nil
}

func (d *RemoteStorage) urlOf(p string) string {
	return d.Endpoint + escapePath(p)
}

func (d *RemoteStorage) dirURL(p string) string {
	u := d.urlOf(p)
	if !strings.HasSuffix(u, "/") {
		u += "/"
	}
	return u
}

func escapePath(p string) string {
	segments := strings.Split(strings.Trim(strings.TrimPrefix(path.Clean("/"+p), "/"), "/"), "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return "/" + strings.Join(segments, "/")
}

const (
	folderAccept      = `application/ld+json; profile="http://www.w3.org/ns/webdav"`
	folderContentType = `application/ld+json; profile="http://www.w3.org/ns/webdav"`
)

type folderItem struct {
	ContentType   string `json:"Content-Type"`
	ContentLength int64  `json:"Content-Length"`
}

type folderListing struct {
	Items map[string]folderItem `json:"items"`
}

var _ driver.Driver = (*RemoteStorage)(nil)
var _ driver.Getter = (*RemoteStorage)(nil)
var _ driver.Mkdir = (*RemoteStorage)(nil)
var _ driver.Move = (*RemoteStorage)(nil)
var _ driver.Rename = (*RemoteStorage)(nil)
var _ driver.Copy = (*RemoteStorage)(nil)
var _ driver.Remove = (*RemoteStorage)(nil)
var _ driver.Put = (*RemoteStorage)(nil)
