package hcbapi

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// DownloadFile fetches a signed file URL (receipt url/preview_url, check
// deposit images, comment attachments, org logos) and writes it into destDir.
// If name is empty, it uses the Content-Disposition filename, falling back to
// the URL's path basename. Signed URLs are self-authorizing, so no
// Authorization header is sent — deliberately, to avoid leaking the bearer
// token to blob/CDN hosts. Returns the saved file's path.
func (c *Client) DownloadFile(ctx context.Context, fileURL, destDir, name string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "hcb-mcp/0.1 (github.com/hackclub/hcb-cli-and-mcp)")
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("downloading %s: HTTP %d", fileURL, resp.StatusCode)
	}

	if name == "" {
		name = filenameFromDisposition(resp.Header.Get("Content-Disposition"))
	}
	if name == "" {
		if u, err := url.Parse(fileURL); err == nil {
			name = path.Base(u.Path)
		}
	}
	if name == "" || name == "/" || name == "." {
		name = "download"
	}
	name = sanitizeFilename(name)

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	destPath := filepath.Join(destDir, name)
	f, err := os.Create(destPath)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(destPath)
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return destPath, nil
}

func filenameFromDisposition(cd string) string {
	if cd == "" {
		return ""
	}
	if _, params, err := mime.ParseMediaType(cd); err == nil {
		if fn := params["filename"]; fn != "" {
			return fn
		}
	}
	return ""
}

// sanitizeFilename strips path separators so a hostile filename can't escape destDir.
func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "_")
	name = filepath.Base(name)
	if name == "." || name == ".." {
		return "download"
	}
	return name
}
