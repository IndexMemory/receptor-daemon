package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// MaxBytesPerFile mirrors MAX_BYTES_PER_FILE in app/api/upload/route.ts.
const MaxBytesPerFile = 50 * 1024 * 1024

type UploadStatus string

const (
	StatusQueued  UploadStatus = "queued"
	StatusDeduped UploadStatus = "deduped"
	StatusError   UploadStatus = "error"
)

type UploadOutcome struct {
	Status   UploadStatus
	ID       string
	Filename string
	Error    string
}

// MemoryUploading abstracts the Memory REST API so SyncEngine can be
// tested with a fake instead of a real network connection.
type MemoryUploading interface {
	TestConnection(ctx context.Context) (bool, error)
	Upload(ctx context.Context, filePath, filename string) (UploadOutcome, error)
}

// MemoryClient talks to Memory's existing POST /api/upload and
// GET /api/auth/me endpoints, authenticated with a static API key minted
// from Memory's web UI (Settings > API Keys). No OAuth here — this daemon
// runs on machines with no browser at all, so there's no login flow, just
// a key pasted into config at `init` time.
type MemoryClient struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

func NewMemoryClient(baseURL, apiKey string) *MemoryClient {
	return &MemoryClient{BaseURL: baseURL, APIKey: apiKey, HTTPClient: http.DefaultClient}
}

// HTTPError is returned for any non-2xx response.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
}

// IsHTTPStatus reports whether err is an *HTTPError with the given status
// code.
func IsHTTPStatus(err error, status int) bool {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == status
	}
	return false
}

func (c *MemoryClient) TestConnection(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, joinURL(c.BaseURL, "api/auth/me"), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300, nil
}

func (c *MemoryClient) Upload(ctx context.Context, filePath, filename string) (UploadOutcome, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return UploadOutcome{}, err
	}
	if info.Size() > MaxBytesPerFile {
		return UploadOutcome{}, fmt.Errorf("file is %d bytes, over the %d-byte cap", info.Size(), int64(MaxBytesPerFile))
	}

	f, err := os.Open(filePath)
	if err != nil {
		return UploadOutcome{}, err
	}
	defer f.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := createFormFile(writer, filename, detectContentType(filePath, filename))
	if err != nil {
		return UploadOutcome{}, err
	}
	if _, err := io.Copy(part, f); err != nil {
		return UploadOutcome{}, err
	}
	if err := writer.Close(); err != nil {
		return UploadOutcome{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(c.BaseURL, "api/upload"), &body)
	if err != nil {
		return UploadOutcome{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return UploadOutcome{}, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return UploadOutcome{}, &HTTPError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	var decoded struct {
		OK      bool `json:"ok"`
		Results []struct {
			Status   UploadStatus `json:"status"`
			ID       string       `json:"id"`
			Filename string       `json:"filename"`
			Error    string       `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return UploadOutcome{}, fmt.Errorf("failed to decode server response: %w", err)
	}
	if len(decoded.Results) == 0 {
		return UploadOutcome{}, errors.New("empty 'results' array in server response")
	}
	r := decoded.Results[0]
	return UploadOutcome{Status: r.Status, ID: r.ID, Filename: r.Filename, Error: r.Error}, nil
}

var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

// createFormFile mirrors multipart.Writer.CreateFormFile, except it lets
// the caller supply the part's Content-Type instead of always hardcoding
// application/octet-stream (CreateFormFile's actual stdlib behavior).
// That default matters here: Memory's upload route trusts the uploaded
// part's Content-Type as-is (`file.type || "application/octet-stream"`
// in app/api/upload/route.ts) and stores it as the document's mime_type,
// which its classification pipeline uses to decide whether to even
// attempt reading the file — application/octet-stream gets treated as an
// opaque, unreadable binary blob regardless of the file's real content.
func createFormFile(writer *multipart.Writer, filename, contentType string) (io.Writer, error) {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files"; filename="%s"`, quoteEscaper.Replace(filename)))
	header.Set("Content-Type", contentType)
	return writer.CreatePart(header)
}

// detectContentType figures out a file's real MIME type: first by
// extension (covers the vast majority of cases), falling back to
// sniffing the first 512 bytes of actual content for extensions Go's
// built-in MIME table doesn't know about (e.g. .log) — still correctly
// identifies plain text instead of defaulting to application/octet-stream.
func detectContentType(filePath, filename string) string {
	if t := mime.TypeByExtension(filepath.Ext(filename)); t != "" {
		return t
	}
	f, err := os.Open(filePath)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	if n == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(buf[:n])
}

func joinURL(base, p string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base + "/" + p
	}
	u.Path = path.Join(u.Path, p)
	return u.String()
}
