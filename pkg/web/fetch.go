package web

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pkg/errors"
	"golang.org/x/net/html"
)

const (
	defaultMaxFetchBytes = 5 << 20 // 5 MiB
	fetchTextLimit       = 30000
	fetchUserAgent       = "golem/1.0 (+https://github.com/user/golem)"
)

// Fetcher retrieves a URL and returns readable text, guarded against SSRF.
type Fetcher struct {
	// MaxBytes caps the downloaded body size.
	MaxBytes int64
	// Timeout bounds the whole request.
	Timeout time.Duration
	// AllowPrivate disables the private-address guard. Only for tests or
	// self-hosting against a local service; leave false in production.
	AllowPrivate bool

	client *http.Client
}

// NewFetcher constructs a Fetcher with the SSRF-guarded transport.
func NewFetcher(maxBytes int64) *Fetcher {
	if maxBytes <= 0 {
		maxBytes = defaultMaxFetchBytes
	}
	return &Fetcher{MaxBytes: maxBytes, Timeout: 20 * time.Second}
}

func (f *Fetcher) httpClient() *http.Client {
	if f.client != nil {
		return f.client
	}
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	f.client = &http.Client{
		Timeout:   timeout,
		Transport: ssrfTransport(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("stopped after 5 redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.Errorf("redirect to unsupported scheme %q", req.URL.Scheme)
			}
			return nil
		},
	}
	return f.client
}

// Fetch retrieves rawURL and returns its readable text, clipped head+tail.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (string, error) {
	target, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", errors.Wrap(err, "invalid URL")
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return "", errors.Errorf("unsupported URL scheme %q (only http/https)", target.Scheme)
	}
	host := target.Hostname()
	if host == "" {
		return "", errors.New("URL has no host")
	}
	if !f.AllowPrivate {
		if err := ensurePublicHost(ctx, host); err != nil {
			return "", err
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", errors.Wrap(err, "building fetch request")
	}
	req.Header.Set("User-Agent", fetchUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json;q=0.9,*/*;q=0.5")

	resp, err := f.httpClient().Do(req)
	if err != nil {
		return "", errors.Wrapf(err, "fetching %s", target.Redacted())
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Drain a bounded prefix so the connection can be reused; the status,
		// not the drain, is what the caller needs to see.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", errors.Errorf("fetch failed: HTTP %d from %s", resp.StatusCode, target.Redacted())
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if !isTextualContentType(contentType) {
		return "", errors.Errorf("unsupported content-type %q from %s", contentType, target.Redacted())
	}

	max := f.MaxBytes
	if max <= 0 {
		max = defaultMaxFetchBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return "", errors.Wrap(err, "reading response body")
	}
	truncated := int64(len(body)) > max
	if truncated {
		body = body[:max]
	}

	var text string
	if strings.Contains(contentType, "html") || strings.Contains(contentType, "xml") {
		text, err = htmlToText(bytes.NewReader(body))
		if err != nil {
			return "", errors.Wrap(err, "extracting text from HTML")
		}
	} else {
		text = string(body)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		text = "(no readable text extracted)"
	}
	if truncated {
		text += "\n\n[... body truncated at " + strconv.FormatInt(max, 10) + " bytes ...]"
	}
	return clipHeadTail(text, fetchTextLimit), nil
}

func isTextualContentType(ct string) bool {
	ct = strings.TrimSpace(ct)
	if ct == "" {
		return true // be permissive when the server omits it
	}
	for _, prefix := range []string{"text/", "application/json", "application/xml", "application/xhtml", "application/rss", "application/atom"} {
		if strings.HasPrefix(ct, prefix) {
			return true
		}
	}
	return strings.Contains(ct, "+json") || strings.Contains(ct, "+xml")
}

// ensurePublicHost resolves host and rejects any non-public address.
func ensurePublicHost(ctx context.Context, host string) error {
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return errors.Errorf("refusing to fetch non-public address %s", ip)
		}
		return nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return errors.Wrapf(err, "resolving %q", host)
	}
	if len(addrs) == 0 {
		return errors.Errorf("no addresses for host %q", host)
	}
	for _, a := range addrs {
		if !isPublicIP(a.IP) {
			return errors.Errorf("refusing to fetch %q: resolves to non-public address %s", host, a.IP)
		}
	}
	return nil
}

// ssrfTransport dials only validated public addresses, so redirects and DNS
// rebinding cannot reach private, loopback, link-local, or metadata endpoints.
func ssrfTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		ForceAttemptHTTP2: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, a := range addrs {
				if isPublicIP(a.IP) {
					return dialer.DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
				}
			}
			return nil, errors.Errorf("refusing to connect to non-public host %q", host)
		},
	}
}

// isPublicIP reports whether ip is a routable, non-private address.
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 { // 100.64.0.0/10 CGNAT
			return false
		}
		if v4[0] == 192 && v4[1] == 0 && v4[2] == 0 { // 192.0.0.0/24
			return false
		}
	}
	return true
}

// htmlToText extracts readable text from HTML, skipping non-content elements.
func htmlToText(r io.Reader) (string, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return "", err
	}

	skip := map[string]bool{"script": true, "style": true, "head": true, "noscript": true, "template": true, "svg": true, "iframe": true}
	block := map[string]bool{"p": true, "div": true, "br": true, "li": true, "tr": true, "section": true, "article": true, "header": true, "footer": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true}

	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if skip[n.Data] {
				return
			}
			if block[n.Data] {
				b.WriteByte('\n')
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && block[n.Data] {
			b.WriteByte('\n')
		}
	}
	walk(doc)

	return normalizeText(b.String()), nil
}

func normalizeText(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		ln = strings.Join(strings.Fields(ln), " ")
		if ln != "" {
			out = append(out, ln)
		}
	}
	return strings.Join(out, "\n")
}

// clipHeadTail keeps the head and the tail of a fetched page in at most limit
// bytes, counting the truncation marker. Cuts are rune-aligned so a page in a
// multi-byte script cannot produce invalid UTF-8.
func clipHeadTail(s string, limit int) string {
	const marker = "\n... [truncated] ...\n"
	if limit <= 0 || len(s) <= limit {
		return s
	}
	budget := limit - len(marker)
	if budget <= 0 {
		return clipLeadingRunes(s, limit)
	}
	head := budget * 2 / 3
	tail := budget - head
	return clipLeadingRunes(s, head) + marker + clipTrailingRunes(s, tail)
}

// clipLeadingRunes returns at most n leading bytes of s, ending on a rune boundary.
func clipLeadingRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// clipTrailingRunes returns at most n trailing bytes of s, starting on a rune boundary.
func clipTrailingRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if n >= len(s) {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}
