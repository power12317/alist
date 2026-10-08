package google_drive_share

import (
	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/op"
)

type Addition struct {
	ShareURL string `json:"share_url" required:"true" help:"Public Google Drive file or folder URL (Anyone with the link); no account or API key needed"`
}

var config = driver.Config{
	Name:        "GoogleDrive Share",
	LocalSort:   true,
	OnlyProxy:   true,
	NoUpload:    true,
	CheckStatus: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver { return &GoogleDriveShare{} })
}
