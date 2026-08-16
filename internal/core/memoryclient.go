package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// RemoteFolder mirrors Memory's ReceptorDaemonConfig.folders[] wire shape
// (lib/api_keys.ts).
type RemoteFolder struct {
	Path           string   `json:"path"`
	IgnorePatterns []string `json:"ignore_patterns"`
}

// RemoteConfig mirrors Memory's ReceptorDaemonConfig — the subset of
// config a receptor-kind API key can remotely control from Memory's web
// UI. ServerURL is deliberately excluded (never remotely settable — see
// README); APIKey rotation is a separate mechanism (see CheckInResult.
// RotateAPIKey below), not part of this struct.
type RemoteConfig struct {
	SyncIntervalMinutes int            `json:"sync_interval_minutes"`
	Folders             []RemoteFolder `json:"folders"`
	BootStartEnabled    bool           `json:"boot_start_enabled"`
}

// CheckInResult is Memory's POST /api/receptor-checkin response.
type CheckInResult struct {
	NeedsUpdate bool
	// Config is only non-nil when NeedsUpdate is true.
	Config  *RemoteConfig
	Version int
	// RotateAPIKey is non-nil when Memory has a key rotation in flight
	// for this key: the daemon should start using this plaintext key for
	// all future requests, including its next check-in. See
	// internal/daemon/checkin.go's applyAPIKeyRotation.
	RotateAPIKey *string
	// UpdateToVersion is non-nil when an admin has remotely triggered an
	// update for this key: the daemon should download and install the
	// latest release via DownloadDaemonBinary (same path as
	// `receptor update`) and restart. Informational string only —
	// see applyReceptorCheckin in Memory's lib/api_keys.ts, the daemon
	// always installs whatever is actually latest at apply time.
	UpdateToVersion *string
}

// CheckIn reports the daemon's currently-running config to Memory and
// asks whether there's a pending remote edit to apply. See Memory's
// lib/api_keys.ts applyReceptorCheckin() for the full protocol: a
// compare-and-swap on version means a stale in-flight check-in can't
// stomp a newer admin edit — worst case, this daemon just sees
// NeedsUpdate again on its next check-in a minute later.
//
// version is this build's own version string (main.version), reported
// alongside the config purely as telemetry — Memory uses it to flag
// out-of-date daemons in the Integrations UI. It's not part of
// RemoteConfig itself since it's never something an admin edit sets;
// it only ever flows daemon → Memory.
//
// updateError, if non-empty, reports that the *previous* check-in's
// remote-update attempt failed (see internal/daemon/checkin.go's
// applyRemoteUpdate) — surfaced in Memory's UI instead of leaving an
// admin staring at a permanently-stuck "Updating" spinner with no idea
// anything's wrong. The daemon keeps retrying every cycle regardless
// (e.g. a permission error blocking a remote update might get fixed by
// a human running `sudo receptor update` themselves, at which
// point the next check-in's report naturally clears this).
func (c *MemoryClient) CheckIn(ctx context.Context, current RemoteConfig, version, updateError string) (CheckInResult, error) {
	body, err := json.Marshal(struct {
		RemoteConfig
		DaemonVersion string `json:"daemon_version"`
		UpdateError   string `json:"update_error,omitempty"`
	}{RemoteConfig: current, DaemonVersion: version, UpdateError: updateError})
	if err != nil {
		return CheckInResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(c.BaseURL, "api/receptor-checkin"), bytes.NewReader(body))
	if err != nil {
		return CheckInResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return CheckInResult{}, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return CheckInResult{}, &HTTPError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	var decoded struct {
		OK              bool          `json:"ok"`
		NeedsUpdate     bool          `json:"needs_update"`
		Config          *RemoteConfig `json:"config"`
		Version         int           `json:"version"`
		RotateAPIKey    *string       `json:"rotate_api_key"`
		UpdateToVersion *string       `json:"update_to_version"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return CheckInResult{}, fmt.Errorf("failed to decode server response: %w", err)
	}
	return CheckInResult{
		NeedsUpdate:     decoded.NeedsUpdate,
		Config:          decoded.Config,
		Version:         decoded.Version,
		RotateAPIKey:    decoded.RotateAPIKey,
		UpdateToVersion: decoded.UpdateToVersion,
	}, nil
}

// MaxBinaryBytes caps how much a single `receptor update` download
// will accept, purely defensive — real release binaries are a few tens of
// MB, so anything past this points at a bug or a misbehaving server, not
// a legitimate release.
const MaxBinaryBytes = 200 * 1024 * 1024

// LatestDaemonVersion asks Memory what the newest published
// receptor release is — see GET /api/receptor-daemon/latest-version
// (lib/receptor_daemon_releases.ts). Empty string if Memory doesn't know
// yet (e.g. its own GitHub lookup hasn't succeeded).
func (c *MemoryClient) LatestDaemonVersion(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, joinURL(c.BaseURL, "api/receptor-daemon/latest-version"), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &HTTPError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	var decoded struct {
		LatestVersion *string `json:"latest_version"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return "", fmt.Errorf("failed to decode server response: %w", err)
	}
	if decoded.LatestVersion == nil {
		return "", nil
	}
	return *decoded.LatestVersion, nil
}

// DaemonBinary is a downloaded, already-checksum-verified receptor
// release binary, ready to be written to disk.
type DaemonBinary struct {
	Version string
	SHA256  string
	Bytes   []byte
}

// DownloadDaemonBinary fetches the latest receptor release binary
// for the given platform through Memory (never directly from GitHub —
// see "Rotating an API key"-style reasoning in the README: this daemon
// only ever needs outbound access to its own Memory server, not to
// github.com too). Memory has already verified the binary against
// GitHub's published checksums.txt before serving it; SHA256 here is a
// second, independent check against transport corruption between Memory
// and this daemon, not a substitute for Memory's own verification.
func (c *MemoryClient) DownloadDaemonBinary(ctx context.Context, goos, goarch string) (DaemonBinary, error) {
	u := joinURL(c.BaseURL, "api/receptor-daemon/download") + "?os=" + url.QueryEscape(goos) + "&arch=" + url.QueryEscape(goarch)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return DaemonBinary{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return DaemonBinary{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return DaemonBinary{}, &HTTPError{StatusCode: resp.StatusCode, Body: string(body)}
	}

	version := resp.Header.Get("X-Receptor-Daemon-Version")
	expectedSHA256 := resp.Header.Get("X-Receptor-Daemon-Sha256")

	limited := io.LimitReader(resp.Body, MaxBinaryBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return DaemonBinary{}, fmt.Errorf("downloading binary: %w", err)
	}
	if len(data) > MaxBinaryBytes {
		return DaemonBinary{}, fmt.Errorf("download exceeded the %d-byte cap", MaxBinaryBytes)
	}

	sum := sha256.Sum256(data)
	gotSHA256 := hex.EncodeToString(sum[:])
	if expectedSHA256 != "" && gotSHA256 != expectedSHA256 {
		return DaemonBinary{}, fmt.Errorf("checksum mismatch: expected %s, got %s — refusing to use this download", expectedSHA256, gotSHA256)
	}

	return DaemonBinary{Version: version, SHA256: gotSHA256, Bytes: data}, nil
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
