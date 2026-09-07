package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The two budgets are different on purpose. The manifest is a few hundred
// bytes and a machine that cannot fetch it in a minute is not going to fetch
// forty megabytes either, so it fails fast. The artifact is the forty
// megabytes, and a tester on a hotel connection is not doing anything wrong;
// it gets a budget long enough to finish and short enough that a stalled
// transfer eventually reports rather than hangs for the afternoon.
const (
	// ConnectTimeout is how long a TCP connection may take to establish.
	ConnectTimeout = 2 * time.Second
	// ManifestTimeout is the whole-request budget for fetching desktop.json.
	ManifestTimeout = 60 * time.Second
	// ArtifactTimeout is the whole-request budget for one artifact.
	ArtifactTimeout = 15 * time.Minute
)

// newHTTPClient builds the client both fetches use. Redirects are followed —
// every GitHub release asset URL is a redirect to object storage — and the
// connect phase is bounded separately from the transfer, so a host that does
// not answer is reported in two seconds rather than at the end of the budget.
func newHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: ConnectTimeout}).DialContext,
			TLSHandshakeTimeout:   ConnectTimeout,
			ResponseHeaderTimeout: 30 * time.Second,
			Proxy:                 http.ProxyFromEnvironment,
		},
	}
}

// fetchManifest reads desktop.json from a URL.
func (o *Options) fetchManifest(ctx context.Context, manifestURL string) (*Manifest, error) {
	ctx, cancel := context.WithTimeout(ctx, ManifestTimeout)
	defer cancel()

	body, err := o.get(ctx, manifestURL, ManifestTimeout)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	// A manifest is small; a base URL that is really an HTML error page is
	// not, and reading it whole would be the one way this command uses real
	// memory. One megabyte is far more than any manifest and far less than a
	// mistake.
	data, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", manifestURL, err)
	}
	return ParseManifest(data)
}

// get issues the GET and returns the body, or an error naming the URL.
func (o *Options) get(ctx context.Context, rawURL string, budget time.Duration) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%s is not a usable URL: %w", rawURL, err)
	}
	req.Header.Set("User-Agent", "sonar-install-desktop")
	resp, err := o.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", rawURL, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, fmt.Errorf("%s returned 404 — is that the right --base or --version?", rawURL)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s returned %s", rawURL, resp.Status)
	}
	return resp.Body, nil
}

// downloadArtifact fetches one artifact into dir and returns the path of the
// file it wrote. The caller owns the file, including removing it.
//
// Verification happens on the way in: the digest is computed from the same
// bytes that reach the disk, so there is no window in which a verified file
// could be replaced before it is used.
func (o *Options) downloadArtifact(ctx context.Context, art Artifact, dir, name string) (string, error) {
	if strings.TrimSpace(art.SHA256) == "" {
		return "", fmt.Errorf("%s has no sha256 for %s — refusing to install an unverifiable download", ManifestName, art.URL)
	}

	ctx, cancel := context.WithTimeout(ctx, ArtifactTimeout)
	defer cancel()

	body, err := o.get(ctx, art.URL, ArtifactTimeout)
	if err != nil {
		return "", err
	}
	defer body.Close()

	tmp, err := os.CreateTemp(dir, ".sonar-desktop-*")
	if err != nil {
		return "", fmt.Errorf("creating a temporary file in %s: %w", dir, err)
	}
	path := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(path)
		}
	}()

	sum := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, sum), o.progressReader(body, art.Size, name))
	o.endProgress()
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", art.URL, err)
	}

	if art.Size > 0 && written != art.Size {
		return "", fmt.Errorf("%s is %d bytes, the manifest says %d — the download did not complete",
			filepath.Base(art.URL), written, art.Size)
	}
	got := hex.EncodeToString(sum.Sum(nil))
	if !strings.EqualFold(got, strings.TrimSpace(art.SHA256)) {
		return "", fmt.Errorf("%s failed its checksum: got %s, the manifest says %s",
			filepath.Base(art.URL), got, strings.ToLower(strings.TrimSpace(art.SHA256)))
	}

	ok = true
	return path, nil
}

// progressReader wraps r with a progress line when the caller asked for one.
// Off a TTY (and under --json) it is the reader itself: a build log does not
// want a hundred carriage returns.
func (o *Options) progressReader(r io.Reader, total int64, name string) io.Reader {
	if !o.Progress || o.Out == nil {
		return r
	}
	return &progressReader{r: r, total: total, name: name, out: o.Out, last: time.Now()}
}

func (o *Options) endProgress() {
	if o.Progress && o.Out != nil {
		fmt.Fprintln(o.Out)
	}
}

type progressReader struct {
	r     io.Reader
	out   io.Writer
	name  string
	total int64
	done  int64
	last  time.Time
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	// Ten frames a second is smooth to a human and cheap on a serial line;
	// redrawing per Read would spend more time formatting than copying.
	if time.Since(p.last) >= 100*time.Millisecond || err == io.EOF {
		p.last = time.Now()
		p.draw()
	}
	return n, err
}

func (p *progressReader) draw() {
	if p.total > 0 {
		fmt.Fprintf(p.out, "\r  %s  %s / %s (%d%%)   ",
			p.name, humanBytes(p.done), humanBytes(p.total), p.done*100/p.total)
		return
	}
	fmt.Fprintf(p.out, "\r  %s  %s   ", p.name, humanBytes(p.done))
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}
