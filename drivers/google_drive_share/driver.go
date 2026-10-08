package google_drive_share

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"time"

	"github.com/alist-org/alist/v3/drivers/base"
	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/errs"
	"github.com/alist-org/alist/v3/internal/model"
	"golang.org/x/net/publicsuffix"
)

type GoogleDriveShare struct {
	model.Storage
	Addition
	client *http.Client
	share  shareRef
}

func (d *GoogleDriveShare) Config() driver.Config { return config }

func (d *GoogleDriveShare) GetAddition() driver.Additional { return &d.Addition }

func (d *GoogleDriveShare) Init(ctx context.Context) error {
	ref, err := parseShareURL(d.ShareURL)
	if err != nil {
		return err
	}
	d.share = ref
	if ref.mime != "" {
		return errNativeFile
	}
	// Use AList's transport/proxy settings, with an isolated anonymous cookie jar.
	client := base.HttpClient
	if client == nil {
		client = http.DefaultClient
	}
	copyClient := *client
	copyClient.Timeout = 30 * time.Second
	copyClient.Jar, err = cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return err
	}
	copyClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("Google Drive share: too many redirects")
		}
		return checkGoogleURL(req.URL)
	}
	d.client = &copyClient
	if ref.folder {
		_, err = d.folderEntries(ctx, ref)
	} else {
		_, err = d.fileInfo(ctx, ref)
	}
	return err
}

func (d *GoogleDriveShare) Drop(ctx context.Context) error { return nil }

// A single-file share is mounted as a directory containing that file.
func (d *GoogleDriveShare) GetRoot(ctx context.Context) (model.Obj, error) {
	return &shareFile{Object: model.Object{ID: d.share.id, IsFolder: true}, ResourceKey: d.share.key}, nil
}

func (d *GoogleDriveShare) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	if !dir.IsDir() {
		return nil, errs.NotFolder
	}
	if !d.share.folder {
		if dir.GetID() != d.share.id {
			return nil, errs.ObjectNotFound
		}
		file, err := d.fileInfo(ctx, d.share)
		if err != nil {
			return nil, err
		}
		return []model.Obj{file}, nil
	}
	ref := d.objectRef(dir)
	entries, err := d.folderEntries(ctx, ref)
	if err != nil {
		return nil, err
	}
	// The embedded view supplies the complete listing, including folders with
	// more than 50 children. The regular page supplies exact sizes and timestamps.
	metadata := map[string]*shareFile{}
	page, pageErr := d.readPage(ctx, folderURL(ref))
	if pageErr == nil {
		metadata, _ = parseFolderMetadata(page)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	objects := make([]model.Obj, 0, len(entries))
	for _, file := range entries {
		if info := metadata[file.ID]; info != nil {
			file.Size, file.Modified, file.Ctime = info.Size, info.Modified, info.Ctime
			file.MimeType = info.MimeType
			if file.ResourceKey == "" {
				file.ResourceKey = info.ResourceKey
			}
		} else if !file.IsDir() && !file.isGoogleNative() {
			// Items beyond the regular page's initial batch still need an exact
			// size for WebDAV, streaming, and server-side copies.
			info, infoErr := d.fileInfo(ctx, d.objectRef(file))
			if infoErr != nil {
				return nil, fmt.Errorf("Google Drive share: metadata for %q: %w", file.Name, infoErr)
			}
			file.Size, file.MimeType = info.Size, info.MimeType
		}
		objects = append(objects, file)
	}
	return objects, nil
}

func (d *GoogleDriveShare) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if file.IsDir() {
		return nil, errs.NotFile
	}
	if info, ok := unwrapFile(file); ok && info.isGoogleNative() {
		return nil, errNativeFile
	}
	downloadURL, header, err := d.resolveDownload(ctx, d.objectRef(file), file.GetSize() != 0)
	if err != nil {
		return nil, err
	}
	expiration := 5 * time.Minute
	return &model.Link{URL: downloadURL, Header: header, Expiration: &expiration}, nil
}

func (d *GoogleDriveShare) objectRef(obj model.Obj) shareRef {
	ref := shareRef{id: obj.GetID(), folder: obj.IsDir()}
	if file, ok := unwrapFile(obj); ok {
		ref.key = file.ResourceKey
	}
	if ref.key == "" && ref.id == d.share.id {
		ref.key = d.share.key
	}
	return ref
}

var _ driver.Driver = (*GoogleDriveShare)(nil)
var _ driver.GetRooter = (*GoogleDriveShare)(nil)
