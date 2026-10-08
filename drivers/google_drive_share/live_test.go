package google_drive_share

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alist-org/alist/v3/internal/model"
)

// Opt in explicitly; routine tests never access Google or store partner files.
func TestLivePublicShare(t *testing.T) {
	shareURL := os.Getenv("GOOGLE_DRIVE_SHARE_TEST_URL")
	if shareURL == "" {
		t.Skip("set GOOGLE_DRIVE_SHARE_TEST_URL to test an actual public share")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d := &GoogleDriveShare{Addition: Addition{ShareURL: shareURL}}
	if err := d.Init(ctx); err != nil {
		t.Fatal(err)
	}
	root, _ := d.GetRoot(ctx)
	files, err := d.List(ctx, root, model.ListArgs{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("anonymous root listing: %d entries", len(files))
	var selected model.Obj
	if path := os.Getenv("GOOGLE_DRIVE_SHARE_TEST_PATH"); path != "" {
		for _, name := range strings.Split(strings.Trim(path, "/"), "/") {
			selected = nil
			for _, file := range files {
				if file.GetName() == name {
					selected = file
					break
				}
			}
			if selected == nil {
				t.Fatalf("path component %q not found", name)
			}
			if selected.IsDir() {
				files, err = d.List(ctx, selected, model.ListArgs{})
				if err != nil {
					t.Fatal(err)
				}
			}
		}
	} else {
		for _, file := range files {
			if !file.IsDir() {
				selected = file
				break
			}
		}
	}
	if selected == nil || selected.IsDir() {
		t.Fatal("set GOOGLE_DRIVE_SHARE_TEST_PATH to a downloadable file within the share")
	}
	link, err := d.Link(ctx, &model.ObjWrapName{Name: selected.GetName(), Obj: selected}, model.LinkArgs{})
	if err != nil {
		t.Fatal(err)
	}
	start := int64(0)
	if raw := os.Getenv("GOOGLE_DRIVE_SHARE_TEST_RANGE_START"); raw != "" {
		start, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
	}
	length := int64(4096)
	if selected.GetSize()-start < length {
		length = selected.GetSize() - start
	}
	if length <= 0 {
		t.Fatal("test range must be within a nonempty file")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, link.URL, nil)
	req.Header = link.Header.Clone()
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, start+length-1))
	res, err := d.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	contentRange := fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, selected.GetSize())
	if res.StatusCode != http.StatusPartialContent || res.Header.Get("Content-Range") != contentRange {
		t.Fatalf("status=%d content-range=%q expected=%q", res.StatusCode, res.Header.Get("Content-Range"), contentRange)
	}
	body, err := readLimited(res.Body, length)
	if err != nil || int64(len(body)) != length {
		t.Fatalf("range length=%d expected=%d err=%v", len(body), length, err)
	}
	t.Logf("anonymous download: %q, total=%d, range=%s, bytes received=%d", selected.GetName(), selected.GetSize(), contentRange, len(body))
	if os.Getenv("GOOGLE_DRIVE_SHARE_TEST_FULL_DOWNLOAD") == "1" {
		// Only enable this for a small test fixture.
		if selected.GetSize() > 1<<20 {
			t.Fatal("full-download test is limited to 1 MiB")
		}
		req, _ = http.NewRequestWithContext(ctx, http.MethodGet, link.URL, nil)
		req.Header = link.Header.Clone()
		res, err = d.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		n, err := io.Copy(io.Discard, io.LimitReader(res.Body, selected.GetSize()+1))
		if err != nil || res.StatusCode != http.StatusOK || n != selected.GetSize() {
			t.Fatalf("full download: status=%d size=%d expected=%d err=%v", res.StatusCode, n, selected.GetSize(), err)
		}
		t.Logf("full anonymous download verified: %d bytes", n)
	}
}
