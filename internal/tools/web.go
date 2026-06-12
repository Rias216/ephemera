package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const maxWebBodyBytes = 2 << 20

// HTTPDoer is the small surface required by web_fetch and allows deterministic tests.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func newSafeWebClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, item := range ips {
				if unsafeIP(item.IP) {
					return nil, fmt.Errorf("web_fetch blocks private or local address %s", item.IP)
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(host, port))
		},
	}
	client := &http.Client{Transport: transport, Timeout: timeout}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("web_fetch redirect limit exceeded")
		}
		return validatePublicURL(req.URL)
	}
	return client
}

func validatePublicURL(target *url.URL) error {
	if target == nil || (target.Scheme != "http" && target.Scheme != "https") {
		return fmt.Errorf("web_fetch requires an http or https URL")
	}
	if target.User != nil {
		return fmt.Errorf("web_fetch rejects URLs containing credentials")
	}
	host := strings.TrimSpace(target.Hostname())
	if host == "" {
		return fmt.Errorf("web_fetch URL has no host")
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return fmt.Errorf("web_fetch blocks localhost")
	}
	if ip := net.ParseIP(host); ip != nil && unsafeIP(ip) {
		return fmt.Errorf("web_fetch blocks private or local address %s", ip)
	}
	return nil
}

func unsafeIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()
}

func (r Registry) webFetch(ctx context.Context, call Call) Result {
	raw := strings.TrimSpace(argString(call, "url"))
	target, err := url.Parse(raw)
	if err != nil {
		return fail(call.Name, "invalid URL: "+err.Error())
	}
	if err := validatePublicURL(target); err != nil {
		return fail(call.Name, err.Error())
	}
	maxChars := argIntDefault(call, "max_chars", 40_000)
	if maxChars < 1_000 {
		maxChars = 1_000
	}
	if maxChars > 200_000 {
		maxChars = 200_000
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return fail(call.Name, err.Error())
	}
	req.Header.Set("User-Agent", "Ephemera/1.0 (+local coding agent)")
	req.Header.Set("Accept", "text/html,application/json,text/plain,text/markdown,application/xml;q=0.8,*/*;q=0.1")
	client := r.WebClient
	if client == nil {
		client = newSafeWebClient(r.CommandTimeout)
	}
	response, err := client.Do(req)
	if err != nil {
		return fail(call.Name, err.Error())
	}
	defer response.Body.Close()
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if !isReadableContentType(contentType) {
		return fail(call.Name, "web_fetch rejected non-text content type "+firstNonEmpty(contentType, "unknown"))
	}
	limited := io.LimitReader(response.Body, maxWebBodyBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return fail(call.Name, err.Error())
	}
	bodyTruncated := len(data) > maxWebBodyBytes
	if bodyTruncated {
		data = data[:maxWebBodyBytes]
	}
	if strings.Contains(contentType, "json") {
		var decoded any
		if json.Unmarshal(data, &decoded) == nil {
			if formatted, marshalErr := json.MarshalIndent(decoded, "", "  "); marshalErr == nil {
				data = formatted
			}
		}
	} else if strings.Contains(contentType, "html") || strings.Contains(strings.ToLower(string(data[:minInt(len(data), 256)])), "<html") {
		data = []byte(htmlToReadableText(string(data)))
	}
	text := strings.TrimSpace(string(data))
	runes := []rune(text)
	charTruncated := len(runes) > maxChars
	if charTruncated {
		text = string(runes[:maxChars]) + "\n\n[web_fetch output truncated]"
	}
	result := ok(call.Name, text)
	result.Metadata = map[string]any{
		"url": target.String(), "status": response.StatusCode, "content_type": contentType,
		"bytes": len(data), "truncated": bodyTruncated || charTruncated,
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.OK = false
		result.Error = fmt.Sprintf("HTTP %d", response.StatusCode)
	}
	return result
}

func isReadableContentType(value string) bool {
	if value == "" {
		return true
	}
	for _, token := range []string{"text/", "json", "xml", "javascript", "x-www-form-urlencoded"} {
		if strings.Contains(value, token) {
			return true
		}
	}
	return false
}

var (
	activeHTMLPattern = regexp.MustCompile(`(?is)<(?:script|style|svg|noscript|template|iframe)[^>]*>.*?</(?:script|style|svg|noscript|template|iframe)>`)
	commentPattern    = regexp.MustCompile(`(?is)<!--.*?-->`)
	tagPattern        = regexp.MustCompile(`(?is)<[^>]+>`)
	spacePattern      = regexp.MustCompile(`[ \t\f\v]+`)
	blankPattern      = regexp.MustCompile(`\n{3,}`)
)

func htmlToReadableText(value string) string {
	value = activeHTMLPattern.ReplaceAllString(value, " ")
	value = commentPattern.ReplaceAllString(value, " ")
	replacer := strings.NewReplacer(
		"</p>", "\n\n", "</div>", "\n", "</li>", "\n", "<br>", "\n", "<br/>", "\n", "<br />", "\n",
		"&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'",
	)
	value = replacer.Replace(value)
	value = tagPattern.ReplaceAllString(value, " ")
	value = strings.ReplaceAll(value, "\r", "")
	lines := strings.Split(value, "\n")
	for index := range lines {
		lines[index] = strings.TrimSpace(spacePattern.ReplaceAllString(lines[index], " "))
	}
	return strings.TrimSpace(blankPattern.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
