package google_drive_share

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/alist-org/alist/v3/drivers/base"
	"github.com/alist-org/alist/v3/internal/model"
	alistnet "github.com/alist-org/alist/v3/internal/net"
	"github.com/alist-org/alist/v3/internal/op"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func response(req *http.Request, status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = http.Header{"Content-Type": {"text/html"}}
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: req}
}

func testDriver(t *testing.T, ref shareRef, transport roundTripFunc) *GoogleDriveShare {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &GoogleDriveShare{share: ref, client: &http.Client{Transport: transport, Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return checkGoogleURL(req.URL)
	}}}
}

func folderEntry(id, name string, folder bool) string {
	path := "/file/d/" + id + "/view"
	if folder {
		path = "/drive/folders/" + id
	}
	return fmt.Sprintf(`<div class="flip-entry"><a href="https://drive.google.com%s"><div class="flip-entry-title">%s</div></a></div>`, path, name)
}

func embeddedPage(entries string) string {
	return `<html><title>Shared folder</title><div id="flip-contents"><div class="flip-entries">` + entries + `</div></div></html>`
}

func metadataRow(id, name, mime string, size any) []any {
	row := make([]any, 115)
	row[0], row[2], row[3], row[13] = id, name, mime, size
	row[9], row[10] = int64(1700000000123), int64(1710000000456)
	row[114] = "https://drive.google.com/file/d/" + id + "/view?resourcekey=child-key"
	return row
}

func metadataPage(rows [][]any) string {
	data, _ := json.Marshal([]any{rows, nil})
	// Google escapes punctuation as hex, and preserves JSON's own escapes.
	escaped := strings.NewReplacer(`\`, `\\`, `'`, `\'`, `"`, `\x22`, `[`, `\x5b`, `]`, `\x5d`, `<`, `\x3c`).Replace(string(data))
	return `<script>window['_DRIVE_ivd'] = '` + escaped + `';</script>`
}

func viewerPage(name string, size any) string {
	item := make([]any, 26)
	item[1], item[11], item[25] = name, "application/octet-stream", []any{nil, nil, size}
	data, _ := json.Marshal(item)
	return `<script>window.viewer = {itemJson: ` + string(data) + `, other: true};</script>`
}

func TestParseShareURL(t *testing.T) {
	for _, tc := range []struct {
		url    string
		folder bool
		key    string
	}{
		{"https://drive.google.com/drive/folders/folder-123?usp=sharing&resourcekey=0-key", true, "0-key"},
		{"https://drive.google.com/drive/u/0/folders/folder-123", true, ""},
		{"https://drive.google.com/file/d/file-123/view", false, ""},
		{"https://drive.google.com/open?id=file-123", false, ""},
		{"https://drive.google.com/uc?export=download&id=file-123", false, ""},
		{"https://drive.google.com/embeddedfolderview?id=folder-123", true, ""},
	} {
		t.Run(tc.url, func(t *testing.T) {
			ref, err := parseShareURL(tc.url)
			if err != nil || ref.id == "" || ref.folder != tc.folder || ref.key != tc.key {
				t.Fatalf("ref=%+v err=%v", ref, err)
			}
		})
	}
	for _, raw := range []string{"", "file-123", "https://evil.example/file/d/id/view", "https://drive.google.com.evil.example/file/d/id/view", "https://user@drive.google.com/file/d/id/view", "https://drive.google.com:8443/file/d/id/view", "https://drive.google.com/file/d/..", "https://drive.google.com/open?id=%27bad", "https://drive.google.com/drive/folders/"} {
		if ref, err := parseShareURL(raw); err == nil {
			t.Errorf("accepted invalid URL %q: %+v", raw, ref)
		}
	}
}

func TestFolderParsing(t *testing.T) {
	files, err := parseFolderEntries([]byte(embeddedPage(folderEntry("folder", "目录 &amp; files", true) + folderEntry("file", " 中文.pdf ", false))))
	if err != nil || len(files) != 2 || !files[0].IsDir() || files[0].Name != "目录 & files" || files[1].Name != " 中文.pdf " {
		t.Fatalf("files=%+v err=%v", files, err)
	}
	files, err = parseFolderEntries([]byte(embeddedPage("")))
	if err != nil || len(files) != 0 {
		t.Fatalf("empty folder: %v, %v", files, err)
	}
	if _, err = parseFolderEntries([]byte(`<html><title>Sign in</title></html>`)); err == nil {
		t.Fatal("login page was treated as an empty folder")
	}
	name := "中文 ' quote \" slash \\ emoji 😀.mp4"
	metadata, err := parseFolderMetadata([]byte(metadataPage([][]any{metadataRow("file", name, "video/mp4", "12345678901")})))
	if err != nil {
		t.Fatal(err)
	}
	file := metadata["file"]
	if file == nil || file.Name != name || file.Size != 12345678901 || file.ResourceKey != "child-key" || file.Modified.UnixMilli() != 1710000000456 {
		t.Fatalf("metadata=%+v", file)
	}
	for _, page := range []string{`<script>window['_DRIVE_ivd']='\x5';</script>`, `<html>no metadata</html>`, `<script>window['_DRIVE_ivd']='{}';</script>`} {
		if _, err = parseFolderMetadata([]byte(page)); err == nil {
			t.Errorf("accepted malformed metadata: %s", page)
		}
	}
}

func TestListBeyondInitialBatch(t *testing.T) {
	var entries strings.Builder
	var rows [][]any
	for i := 0; i < 61; i++ {
		id := fmt.Sprintf("file-%d", i)
		entries.WriteString(folderEntry(id, id+".bin", false))
		if i < 50 {
			rows = append(rows, metadataRow(id, id+".bin", "application/octet-stream", 1000+i))
		}
	}
	viewerRequests := 0
	d := testDriver(t, shareRef{id: "root", folder: true, key: "root-key"}, func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "" {
			t.Fatal("anonymous listing sent authorization")
		}
		switch req.URL.Path {
		case "/embeddedfolderview":
			if req.URL.Query().Get("resourcekey") != "root-key" {
				t.Fatal("root resource key lost")
			}
			return response(req, 200, embeddedPage(entries.String()), nil), nil
		case "/drive/folders/root":
			return response(req, 200, metadataPage(rows), nil), nil
		default:
			viewerRequests++
			return response(req, 200, viewerPage("extra.bin", "4096"), nil), nil
		}
	})
	root, _ := d.GetRoot(context.Background())
	files, err := d.List(context.Background(), root, model.ListArgs{})
	if err != nil || len(files) != 61 || viewerRequests != 11 {
		t.Fatalf("files=%d viewer requests=%d err=%v", len(files), viewerRequests, err)
	}
	if files[49].GetSize() != 1049 || files[60].GetSize() != 4096 {
		t.Fatal("file sizes from both metadata sources were not preserved")
	}
}

func TestSingleFileShare(t *testing.T) {
	old := base.HttpClient
	base.HttpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Get("resourcekey") != "file-key" {
			t.Fatal("single-file resource key lost")
		}
		if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
			t.Fatal("new driver did not start anonymously")
		}
		return response(req, 200, viewerPage("single.pdf", "49338"), nil), nil
	})}
	t.Cleanup(func() { base.HttpClient = old })
	d := &GoogleDriveShare{Addition: Addition{ShareURL: "https://drive.google.com/file/d/file/view?resourcekey=file-key"}}
	if err := d.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	root, _ := d.GetRoot(context.Background())
	files, err := d.List(context.Background(), root, model.ListArgs{})
	if err != nil || !root.IsDir() || len(files) != 1 || files[0].GetName() != "single.pdf" || files[0].GetSize() != 49338 {
		t.Fatalf("single file mount: %v, %v", files, err)
	}
	if _, err = d.List(context.Background(), files[0], model.ListArgs{}); err == nil {
		t.Fatal("listed a file as a folder")
	}
	for _, page := range []string{viewerPage("a", nil), viewerPage("a", "-1"), `<script>itemJson: [];</script>`} {
		if _, err = parseFileInfo([]byte(page), shareRef{id: "file"}); err == nil {
			t.Fatal("accepted invalid viewer metadata")
		}
	}
}

func TestInitWithoutGlobalClient(t *testing.T) {
	oldBase, oldDefault := base.HttpClient, http.DefaultClient
	base.HttpClient = nil
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return response(req, 200, embeddedPage(""), nil), nil
	})}
	t.Cleanup(func() { base.HttpClient, http.DefaultClient = oldBase, oldDefault })
	d := &GoogleDriveShare{Addition: Addition{ShareURL: "https://drive.google.com/drive/folders/root"}}
	if err := d.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.client == http.DefaultClient || d.client.Jar == nil {
		t.Fatal("driver did not create its own anonymous client")
	}
	d = &GoogleDriveShare{}
	if _, err := d.request(context.Background(), "https://drive.google.com/", false); err == nil {
		t.Fatal("uninitialized driver should return an error")
	}
}

func TestDownloadConfirmationAndCookies(t *testing.T) {
	requests := 0
	d := testDriver(t, shareRef{id: "folder", folder: true}, func(req *http.Request) (*http.Response, error) {
		requests++
		if req.Header.Get("Range") != "bytes=0-0" || req.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected probe headers: %v", req.Header)
		}
		if req.URL.Query().Get("resourcekey") != "file-key" {
			t.Fatal("resource key lost during download")
		}
		if req.URL.Host == "drive.google.com" {
			return response(req, 303, "", http.Header{"Location": {"https://drive.usercontent.google.com/download?id=file&resourcekey=file-key"}}), nil
		}
		if req.URL.Query().Get("confirm") != "token" {
			return response(req, 200, `<form id="download-form" action="https://drive.usercontent.google.com/download?export=download"><input value="file" name="id" type="hidden"><input type="hidden" name="confirm" value="token"><input name="uuid" value="unique-value" type="hidden"></form>`, http.Header{"Content-Type": {"text/html"}, "Set-Cookie": {"anonymous=temporary; Path=/; Secure"}}), nil
		}
		if req.URL.Query().Get("uuid") != "unique-value" || req.Header.Get("Cookie") != "anonymous=temporary" {
			t.Fatal("confirmation form or anonymous cookies not retained")
		}
		return response(req, 206, "x", http.Header{"Content-Disposition": {`attachment; filename="movie.mp4"`}, "Content-Range": {"bytes 0-0/1000000000"}}), nil
	})
	file := &model.ObjWrapName{Name: "movie.mp4", Obj: &shareFile{Object: model.Object{ID: "file", Size: 1000000000}, ResourceKey: "file-key"}}
	link, err := d.Link(context.Background(), file, model.LinkArgs{})
	if err != nil || requests != 3 {
		t.Fatalf("link=%+v requests=%d err=%v", link, requests, err)
	}
	if link.Header.Get("Cookie") != "anonymous=temporary" || link.Header.Get("Range") != "" || link.Expiration == nil || *link.Expiration != 5*time.Minute || !strings.Contains(link.URL, "confirm=token") {
		t.Fatalf("invalid proxy link: %+v", link)
	}
	proxied := alistnet.ProcessHeader(http.Header{"Authorization": {"Basic private-alist-credentials"}, "Cookie": {"alist_token=private"}}, link.Header)
	if proxied.Get("Authorization") != "" || proxied.Get("Cookie") != "anonymous=temporary" {
		t.Fatal("AList credentials would be forwarded to Google")
	}
}

func TestConfirmationVariants(t *testing.T) {
	current, _ := url.Parse("https://drive.google.com/uc?id=file")
	for _, page := range []string{
		`<a href="/uc?export=download&amp;id=file&amp;confirm=t">Download anyway</a>`,
		`<script>{"downloadUrl":"https:\/\/drive.usercontent.google.com\/download?id\u003dfile\u0026confirm\u003dt"}</script>`,
	} {
		raw, err := confirmationURL([]byte(page), current, shareRef{id: "file", key: "key"})
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(raw)
		if u.Query().Get("confirm") != "t" || u.Query().Get("resourcekey") != "key" {
			t.Fatal(raw)
		}
	}
	for _, page := range []string{
		`<form id="download-form" action="https://evil.example/download"><input type="hidden" name="id" value="file"></form>`,
		`<form id="download-form" action="http://127.0.0.1/download"></form>`,
		`<form id="download-form" action="https://drive.usercontent.google.com/download?id=other"></form>`,
		`<form id="download-form"></form>`,
	} {
		if _, err := confirmationURL([]byte(page), current, shareRef{id: "file"}); err == nil {
			t.Fatal("accepted unsafe confirmation: " + page)
		}
	}
}

func TestDownloadErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body, message string
		status              int
		header              http.Header
	}{
		{"quota", `<p class="uc-error-subcaption">Too many users have viewed or downloaded this file recently.</p>`, "Too many users", 200, nil},
		{"permission", "Access denied", "HTTP 403", 403, nil},
		{"login", "", "sign-in required", 302, http.Header{"Location": {"https://accounts.google.com/ServiceLogin"}}},
		{"not-a-file", "<html>Preview</html>", "cannot access", 200, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := testDriver(t, shareRef{id: "file"}, func(req *http.Request) (*http.Response, error) {
				return response(req, tc.status, tc.body, tc.header), nil
			})
			_, _, err := d.resolveDownload(context.Background(), shareRef{id: "file"}, true)
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	d := testDriver(t, shareRef{}, func(req *http.Request) (*http.Response, error) { return nil, req.Context().Err() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := d.resolveDownload(ctx, shareRef{id: "file"}, true); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestEmptyFileAndHTMLAttachment(t *testing.T) {
	for _, size := range []int64{0, 42} {
		d := testDriver(t, shareRef{id: "file"}, func(req *http.Request) (*http.Response, error) {
			if size == 0 && req.Header.Get("Range") != "" {
				t.Fatal("a range probe on an empty file would return 416")
			}
			return response(req, 200, "", http.Header{"Content-Type": {"text/html"}, "Content-Disposition": {`attachment; filename="index.html"`}}), nil
		})
		link, err := d.Link(context.Background(), &shareFile{Object: model.Object{ID: "file", Size: size}}, model.LinkArgs{})
		if err != nil {
			t.Fatal(err)
		}
		proxied := alistnet.ProcessHeader(http.Header{"Authorization": {"private"}, "Cookie": {"private"}}, link.Header)
		if proxied.Get("Cookie") != "" || proxied.Get("Authorization") != "" {
			t.Fatal("anonymous link inherited private downstream headers")
		}
	}
}

func TestDriverRegistration(t *testing.T) {
	constructor, err := op.GetDriver("GoogleDrive Share")
	if err != nil || !constructor().Config().OnlyProxy || !constructor().Config().NoUpload {
		t.Fatalf("driver registration: %v", err)
	}
}

func TestTransientRequestFailure(t *testing.T) {
	attempts := 0
	d := testDriver(t, shareRef{}, func(req *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return nil, io.EOF
		}
		return response(req, 200, embeddedPage(""), nil), nil
	})
	if _, err := d.folderEntries(context.Background(), shareRef{id: "root"}); err != nil || attempts != 2 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}
