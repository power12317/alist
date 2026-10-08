package google_drive_share

import (
	"errors"
	"strings"

	"github.com/alist-org/alist/v3/internal/model"
)

const folderMime = "application/vnd.google-apps.folder"

var errNativeFile = errors.New("Google Drive share: Google native documents and shortcuts require export or target resolution, which this driver does not support")

type shareRef struct {
	id     string
	key    string
	folder bool
	mime   string
}

type shareFile struct {
	model.Object
	ResourceKey string
	MimeType    string
}

func (f *shareFile) isGoogleNative() bool {
	return !f.IsDir() && strings.HasPrefix(f.MimeType, "application/vnd.google-apps.")
}

func unwrapFile(obj model.Obj) (*shareFile, bool) {
	for {
		if file, ok := obj.(*shareFile); ok {
			return file, true
		}
		wrapped, ok := obj.(model.ObjUnwrap)
		if !ok {
			return nil, false
		}
		obj = wrapped.Unwrap()
	}
}
