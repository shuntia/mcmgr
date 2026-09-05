// Package httpx wraps net/http with the timeouts, user agent, retry policy and
// checksum-verified downloads the provider clients share.
package httpx

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"mcmgr/internal/ui"
)

// fallbackVersion is the release this source tree is, used when the binary
// carries no build information — a plain `go build` outside a repository.
const fallbackVersion = "0.1.0"

// Version is the mcmgr build, as reported by `mcmgr version` and sent in the
// User-Agent. It is derived from the linker's build info so a bug report names
// a commit rather than a constant nobody bumped.
var Version = buildVersion(debug.ReadBuildInfo())

// UserAgent identifies mcmgr to the APIs it calls. Modrinth requires a
// descriptive agent and will rate-limit generic ones harder.
var UserAgent = "mcmgr/" + Version + " (github.com/shuntia/mcmgr)"

// buildVersion formats the module version with the revision it was built from,
// e.g. "0.1.0+9f3c1ab" or "0.1.0+9f3c1ab+dirty". It takes the build info as
// arguments so the formatting is testable.
func buildVersion(info *debug.BuildInfo, ok bool) string {
	if !ok || info == nil {
		return fallbackVersion
	}

	// Cut any build metadata the toolchain already appended, so it is not
	// repeated below.
	version, _, _ := strings.Cut(strings.TrimPrefix(info.Main.Version, "v"), "+")
	// "(devel)" and a 0.0.0- pseudo-version are what a build from a working
	// tree reports. Neither says more than the release the source claims to
	// be, and the revision below is what actually identifies the build.
	if version == "" || version == "(devel)" || strings.HasPrefix(version, "0.0.0-") {
		version = fallbackVersion
	}

	var revision string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if len(revision) > 7 {
		revision = revision[:7]
	}
	if revision != "" {
		version += "+" + revision
	}
	if dirty {
		version += "+dirty"
	}
	return version
}

var client = &http.Client{
	Timeout: 10 * time.Minute, // generous: covers large jar downloads
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConnsPerHost:   8,
		ForceAttemptHTTP2:     true,
	},
}

// StatusError is returned for non-2xx responses.
type StatusError struct {
	Code int
	URL  string
	Body string
}

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("HTTP %d from %s", e.Code, e.URL)
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}

// NotFound reports whether err is a 404.
func NotFound(err error) bool {
	se, ok := err.(*StatusError)
	return ok && se.Code == http.StatusNotFound
}

func do(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", UserAgent)

	var lastErr error
	// Three attempts, backing off, for transient failures and rate limits.
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 700 * time.Millisecond)
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			lastErr = &StatusError{Code: resp.StatusCode, URL: req.URL.String(), Body: strings.TrimSpace(string(body))}
			if wait := resp.Header.Get("Retry-After"); wait != "" && attempt < 2 {
				if secs, perr := time.ParseDuration(wait + "s"); perr == nil && secs < 30*time.Second {
					time.Sleep(secs)
				}
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return nil, &StatusError{Code: resp.StatusCode, URL: req.URL.String(), Body: strings.TrimSpace(string(body))}
		}
		return resp, nil
	}
	return nil, lastErr
}

// GetJSON fetches u and decodes the JSON body into out. Extra headers, such as
// CurseForge's x-api-key, may be supplied.
func GetJSON(u string, out any, headers map[string]string) error {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// GetText fetches a small plain-text document, such as a published checksum.
// The body is capped, since nothing this is used for is more than a line.
func GetText(u string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}

// PostJSON sends body as JSON to u and decodes the response into out.
func PostJSON(u string, body, out any, headers map[string]string) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, u, strings.NewReader(string(buf)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// Download streams u into dst, showing a progress bar labelled with the
// destination's base name. When sum is non-empty the download is verified
// against it (sha1 for 40 hex chars, sha512 for 128) and discarded on
// mismatch. The file is written to a temporary path and renamed into place so
// an interrupted download never leaves a half-written jar behind.
func Download(u, dst, sum string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".mcmgr-dl-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // no-op once renamed
	}()

	// The hash algorithm is inferred from the digest length, since providers
	// publish different ones: Mojang sha1, PaperMC sha256, Modrinth both.
	var h hash.Hash
	switch len(sum) {
	case 40:
		h = sha1.New()
	case 64:
		h = sha256.New()
	case 128:
		h = sha512.New()
	}

	bar := ui.NewProgress(filepath.Base(dst), resp.ContentLength)
	var w io.Writer = tmp
	if h != nil {
		w = io.MultiWriter(tmp, h)
	}
	_, err = io.Copy(io.MultiWriter(w, bar), resp.Body)
	bar.Done()
	if err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if h != nil {
		if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, sum) {
			return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", filepath.Base(dst), sum, got)
		}
	}
	// os.Rename replaces an existing file on every platform, so the old
	// jar stays in place until the new one is complete and verified.
	return os.Rename(tmpName, dst)
}

// FileSHA1 returns the hex sha1 of a file on disk.
func FileSHA1(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Query builds an escaped query string from ordered key/value pairs.
func Query(pairs ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		v.Add(pairs[i], pairs[i+1])
	}
	return v.Encode()
}
