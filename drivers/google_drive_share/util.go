package google_drive_share

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/alist-org/alist/v3/drivers/base"
	"github.com/alist-org/alist/v3/internal/model"
)

var (
	idPattern     = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	folderDataRE  = regexp.MustCompile(`(?s)(?:window\[['"]_DRIVE_ivd['"]\]|_DRIVE_ivd)\s*=\s*'((?:\\.|[^'\\])*)'`)
	viewerDataRE  = regexp.MustCompile(`\bitemJson\s*:\s*`)
	downloadURLRE = regexp.MustCompile(`"downloadUrl"\s*:\s*("(?:\\.|[^"\\])*")`)
)

func parseShareURL(raw string) (shareRef, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Port() != "" {
		return shareRef{}, fmt.Errorf("Google Drive share: invalid share URL")
	}
	host := strings.ToLower(u.Hostname())
	if host != "drive.google.com" && host != "docs.google.com" {
		return shareRef{}, fmt.Errorf("Google Drive share: expected a drive.google.com share URL")
	}
	ref := shareRef{key: u.Query().Get("resourcekey")}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, part := range parts {
		if part == "folders" && i+1 < len(parts) && host == "drive.google.com" {
			ref.id, ref.folder = parts[i+1], true
			break
		}
		if part == "d" && i+1 < len(parts) {
			ref.id = parts[i+1]
			break
		}
	}
	if ref.id == "" && host == "drive.google.com" {
		ref.id = u.Query().Get("id")
		ref.folder = u.Path == "/embeddedfolderview"
	}
	if host == "docs.google.com" && len(parts) > 0 {
		switch parts[0] {
		case "document", "spreadsheets", "presentation", "drawings", "forms":
			ref.mime = "application/vnd.google-apps." + strings.TrimSuffix(parts[0], "s")
		default:
			return shareRef{}, fmt.Errorf("Google Drive share: unsupported Google Docs URL")
		}
	}
	if !idPattern.MatchString(ref.id) || (ref.key != "" && !idPattern.MatchString(ref.key)) {
		return shareRef{}, fmt.Errorf("Google Drive share: missing or invalid file/folder ID or resource key")
	}
	return ref, nil
}

func checkGoogleURL(u *url.URL) error {
	if u.Hostname() == "accounts.google.com" {
		return fmt.Errorf("Google Drive share: sign-in required; set sharing to Anyone with the link")
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "https" || u.User != nil || u.Port() != "" || !(host == "drive.google.com" || host == "docs.google.com" || host == "drive.usercontent.google.com" || host == "googleusercontent.com" || strings.HasSuffix(host, ".googleusercontent.com")) {
		return fmt.Errorf("Google Drive share: unexpected URL host or scheme")
	}
	return nil
}

func addResourceKey(u *url.URL, key string) string {
	q := u.Query()
	if key != "" {
		q.Set("resourcekey", key)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func folderURL(ref shareRef) string {
	u := &url.URL{Scheme: "https", Host: "drive.google.com", Path: "/drive/folders/" + ref.id, RawQuery: "hl=en"}
	return addResourceKey(u, ref.key)
}

func (d *GoogleDriveShare) request(ctx context.Context, rawURL string, probe bool) (*http.Response, error) {
	if d.client == nil {
		return nil, fmt.Errorf("Google Drive share: storage is not initialized")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if err = checkGoogleURL(req.URL); err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", base.UserAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if probe {
		// Never buffer or download the whole file just to resolve its link.
		req.Header.Set("Range", "bytes=0-0")
	}
	for attempt := 0; ; attempt++ {
		res, requestErr := d.client.Do(req.Clone(ctx))
		var networkErr net.Error
		retryable := errors.Is(requestErr, io.EOF) || errors.Is(requestErr, io.ErrUnexpectedEOF) || (errors.As(requestErr, &networkErr) && networkErr.Timeout())
		if requestErr == nil || !retryable || attempt >= 2 || ctx.Err() != nil {
			return res, requestErr
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func readLimited(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = fmt.Errorf("Google Drive share: response page exceeds %d bytes", limit)
	}
	return data, err
}

func (d *GoogleDriveShare) readPage(ctx context.Context, rawURL string) ([]byte, error) {
	res, err := d.request(ctx, rawURL, false)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	page, err := readLimited(res.Body, 16<<20)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, pageError(page, res.StatusCode)
	}
	return page, nil
}

func (d *GoogleDriveShare) folderEntries(ctx context.Context, ref shareRef) ([]*shareFile, error) {
	u := &url.URL{Scheme: "https", Host: "drive.google.com", Path: "/embeddedfolderview"}
	q := url.Values{"id": {ref.id}, "hl": {"en"}}
	u.RawQuery = q.Encode()
	page, err := d.readPage(ctx, addResourceKey(u, ref.key))
	if err != nil {
		return nil, err
	}
	return parseFolderEntries(page)
}

func parseFolderEntries(page []byte) ([]*shareFile, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(page))
	if err != nil {
		return nil, err
	}
	if doc.Find("#flip-contents .flip-entries, #flip-contents .flip-empty").Length() == 0 {
		return nil, pageError(page, http.StatusOK)
	}
	files := make([]*shareFile, 0)
	seen := map[string]bool{}
	doc.Find(".flip-entry").EachWithBreak(func(_ int, entry *goquery.Selection) bool {
		href, _ := entry.Find("a[href]").First().Attr("href")
		ref, parseErr := parseShareURL(href)
		if parseErr != nil {
			err = fmt.Errorf("Google Drive share: unrecognized folder entry: %w", parseErr)
			return false
		}
		name := entry.Find(".flip-entry-title").Text()
		if name == "" {
			err = fmt.Errorf("Google Drive share: folder entry has no name")
			return false
		}
		if !seen[ref.id] {
			files = append(files, &shareFile{Object: model.Object{ID: ref.id, Name: name, IsFolder: ref.folder}, ResourceKey: ref.key, MimeType: ref.mime})
			seen[ref.id] = true
		}
		return true
	})
	return files, err
}

// Convert a JS single-quoted string to JSON string syntax without executing JS.
func decodeJSString(raw string) (string, error) {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			i++
			if i == len(raw) {
				return "", fmt.Errorf("Google Drive share: truncated JavaScript string")
			}
			switch raw[i] {
			case 'x':
				if i+2 >= len(raw) {
					return "", fmt.Errorf("Google Drive share: truncated hex escape")
				}
				b.WriteString(`\u00`)
				b.WriteString(raw[i+1 : i+3])
				i += 2
			case '\'':
				b.WriteByte('\'')
			default:
				b.WriteByte('\\')
				b.WriteByte(raw[i])
			}
		default:
			b.WriteByte(raw[i])
		}
	}
	b.WriteByte('"')
	var decoded string
	err := json.Unmarshal([]byte(b.String()), &decoded)
	return decoded, err
}

func parseFolderMetadata(page []byte) (map[string]*shareFile, error) {
	m := folderDataRE.FindSubmatch(page)
	if m == nil {
		return nil, fmt.Errorf("Google Drive share: folder metadata is missing")
	}
	data, err := decodeJSString(string(m[1]))
	if err != nil {
		return nil, err
	}
	var listing []json.RawMessage
	if err = json.Unmarshal([]byte(data), &listing); err != nil || len(listing) == 0 {
		return nil, fmt.Errorf("Google Drive share: invalid folder metadata")
	}
	var rows [][]json.RawMessage
	if err = json.Unmarshal(listing[0], &rows); err != nil {
		return nil, err
	}
	files := make(map[string]*shareFile, len(rows))
	for _, row := range rows {
		if len(row) < 14 {
			continue
		}
		id, name, mime := jsonString(row[0]), jsonString(row[2]), jsonString(row[3])
		if !idPattern.MatchString(id) || name == "" || mime == "" {
			continue
		}
		size, sizeErr := jsonInt(row[13])
		if mime != folderMime && !strings.HasPrefix(mime, "application/vnd.google-apps.") && sizeErr != nil {
			continue // Fall back to the file viewer instead of claiming size zero.
		}
		created, _ := jsonInt(row[9])
		modified, _ := jsonInt(row[10])
		file := &shareFile{Object: model.Object{ID: id, Name: name, Size: size, IsFolder: mime == folderMime}, MimeType: mime}
		if created > 0 {
			file.Ctime = time.UnixMilli(created)
		}
		if modified > 0 {
			file.Modified = time.UnixMilli(modified)
		}
		if len(row) > 114 {
			if ref, parseErr := parseShareURL(jsonString(row[114])); parseErr == nil {
				file.ResourceKey = ref.key
			}
		}
		files[id] = file
	}
	return files, nil
}

func jsonString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func jsonInt(raw json.RawMessage) (int64, error) {
	s := strings.Trim(string(raw), `"`)
	v, err := strconv.ParseInt(s, 10, 64)
	if v < 0 {
		err = fmt.Errorf("negative file size or timestamp")
	}
	return v, err
}

func (d *GoogleDriveShare) fileInfo(ctx context.Context, ref shareRef) (*shareFile, error) {
	u := &url.URL{Scheme: "https", Host: "drive.google.com", Path: "/file/d/" + ref.id + "/view", RawQuery: "hl=en"}
	page, err := d.readPage(ctx, addResourceKey(u, ref.key))
	if err != nil {
		return nil, err
	}
	return parseFileInfo(page, ref)
}

func parseFileInfo(page []byte, ref shareRef) (*shareFile, error) {
	loc := viewerDataRE.FindIndex(page)
	if loc == nil {
		return nil, pageError(page, http.StatusOK)
	}
	var item []json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(page[loc[1]:])).Decode(&item); err != nil || len(item) <= 25 {
		return nil, fmt.Errorf("Google Drive share: invalid file viewer metadata")
	}
	name, mime := jsonString(item[1]), jsonString(item[11])
	if name == "" || mime == "" {
		return nil, fmt.Errorf("Google Drive share: missing file name or MIME type")
	}
	var sizes []json.RawMessage
	if err := json.Unmarshal(item[25], &sizes); err != nil || len(sizes) < 3 {
		return nil, fmt.Errorf("Google Drive share: file size is unavailable")
	}
	size, err := jsonInt(sizes[2])
	if err != nil {
		return nil, fmt.Errorf("Google Drive share: invalid file size: %w", err)
	}
	return &shareFile{Object: model.Object{ID: ref.id, Name: name, Size: size}, ResourceKey: ref.key, MimeType: mime}, nil
}

func (d *GoogleDriveShare) resolveDownload(ctx context.Context, ref shareRef, probe bool) (string, http.Header, error) {
	u := &url.URL{Scheme: "https", Host: "drive.google.com", Path: "/uc"}
	u.RawQuery = url.Values{"id": {ref.id}, "export": {"download"}}.Encode()
	rawURL := addResourceKey(u, ref.key)
	seen := map[string]bool{}
	for step := 0; step < 5; step++ {
		if seen[rawURL] {
			break
		}
		seen[rawURL] = true
		res, err := d.request(ctx, rawURL, probe)
		if err != nil {
			return "", nil, err
		}
		if (res.StatusCode == http.StatusOK || res.StatusCode == http.StatusPartialContent) && res.Header.Get("Content-Disposition") != "" {
			res.Body.Close()
			// Override incoming AList/WebDAV credentials even when the instance's
			// proxy header filter has been customized. Only anonymous Google
			// cookies from this storage's jar may reach the upstream download.
			header := http.Header{"User-Agent": {base.UserAgent}, "Authorization": nil, "Cookie": nil}
			if cookies := d.client.Jar.Cookies(res.Request.URL); len(cookies) > 0 {
				cookieReq := &http.Request{Header: http.Header{}}
				for _, cookie := range cookies {
					cookieReq.AddCookie(cookie)
				}
				header.Set("Cookie", cookieReq.Header.Get("Cookie"))
			}
			return res.Request.URL.String(), header, nil
		}
		page, readErr := readLimited(res.Body, 4<<20)
		res.Body.Close()
		if readErr != nil {
			return "", nil, readErr
		}
		if res.StatusCode != http.StatusOK {
			return "", nil, pageError(page, res.StatusCode)
		}
		next, err := confirmationURL(page, res.Request.URL, ref)
		if err != nil {
			return "", nil, err
		}
		rawURL = next
	}
	return "", nil, fmt.Errorf("Google Drive share: download confirmation did not produce a file")
}

func confirmationURL(page []byte, current *url.URL, ref shareRef) (string, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(page))
	if err != nil {
		return "", err
	}
	var next *url.URL
	if form := doc.Find("form#download-form").First(); form.Length() > 0 {
		action, _ := form.Attr("action")
		if action == "" {
			return "", fmt.Errorf("Google Drive share: download form has no action")
		}
		next, err = current.Parse(action)
		if err != nil {
			return "", err
		}
		q := next.Query()
		form.Find("input[name]").Each(func(_ int, input *goquery.Selection) {
			if kind, _ := input.Attr("type"); strings.EqualFold(kind, "hidden") {
				name, _ := input.Attr("name")
				value, _ := input.Attr("value")
				q.Set(name, value)
			}
		})
		next.RawQuery = q.Encode()
	} else if href, ok := doc.Find(`a[href*="export=download"]`).First().Attr("href"); ok {
		next, err = current.Parse(href)
	} else if match := downloadURLRE.FindSubmatch(page); match != nil {
		var raw string
		err = json.Unmarshal(match[1], &raw)
		if err == nil {
			next, err = current.Parse(raw)
		}
	} else {
		return "", pageError(page, http.StatusOK)
	}
	if err != nil {
		return "", err
	}
	if err = checkGoogleURL(next); err != nil {
		return "", err
	}
	if id := next.Query().Get("id"); id != "" && id != ref.id {
		return "", fmt.Errorf("Google Drive share: download confirmation refers to a different file")
	}
	return addResourceKey(next, ref.key), nil
}

func pageError(page []byte, status int) error {
	detail := "cannot access this public share; check Anyone with the link, download permission, and Google's download quota"
	if doc, err := goquery.NewDocumentFromReader(bytes.NewReader(page)); err == nil {
		text := strings.TrimSpace(doc.Find(".uc-error-subcaption, .uc-error-caption, #uc-error-caption").First().Text())
		if text != "" {
			runes := []rune(strings.Join(strings.Fields(text), " "))
			if len(runes) > 300 {
				runes = runes[:300]
			}
			detail = string(runes)
		}
	}
	return fmt.Errorf("Google Drive share (HTTP %d): %s", status, detail)
}
