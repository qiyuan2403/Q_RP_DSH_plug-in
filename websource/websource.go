// Package websource fetches reference material for card authoring.
//
// Two operations, kept apart because they have different trust properties:
//
//   - Search returns candidate results from a search API. It is a thin client
//     over a configured provider and does nothing clever.
//   - Fetch retrieves one page and reduces it to text. It is the operation that
//     touches arbitrary URLs, so it enforces the guards that make that safe:
//     an allowlist of schemes, a refusal to follow redirects to a different
//     origin, a size cap, and a timeout.
//
// The text reduction is intentionally crude (tag stripping, entity decoding).
// A card author needs the prose, and a full HTML engine would be a large
// dependency for a task that does not need one.
package websource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ErrNoProvider reports a search call with no configured provider.
var ErrNoProvider = errors.New("websource: no search provider configured")

// ErrUnsupportedScheme reports a URL whose scheme is not allowed.
var ErrUnsupportedScheme = errors.New("websource: only http and https are allowed")

// Config describes the fetch side.
type Config struct {
	// Timeout bounds one request.
	Timeout time.Duration
	// MaxBytes caps the response body. A page larger than this is truncated
	// rather than rejected: a card author wants the prose, and the prose is at
	// the top.
	MaxBytes int64
	// UserAgent identifies the client. Empty uses a plain default.
	UserAgent string
	// HTTPClient overrides the client. Nil builds one from Timeout.
	HTTPClient *http.Client
	// SearchEndpoint is the search API base. Empty disables Search.
	SearchEndpoint string
	// SearchAPIKey authorizes search.
	SearchAPIKey string
	// SearchResults caps how many results a query returns.
	SearchResults int
}

// Fetcher retrieves and reduces pages.
type Fetcher struct {
	cfg Config
}

// New builds a fetcher.
func New(cfg Config) *Fetcher {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = 4 << 20
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "Q_RP_DSH_plug-in/websource"
	}
	if cfg.SearchResults <= 0 {
		cfg.SearchResults = 5
	}
	return &Fetcher{cfg: cfg}
}

// Page is a fetched document reduced to text.
type Page struct {
	// URL is the final URL after redirects.
	URL string
	// Title is the document title, when one could be extracted.
	Title string
	// Text is the readable text.
	Text string
	// Truncated reports whether the body hit the size cap.
	Truncated bool
	// StatusCode is the final response status.
	StatusCode int
}

// Fetch retrieves one URL and reduces it to text.
//
// Redirects are followed only within the SAME ORIGIN. A page that redirects
// elsewhere is refused: a card author following a link expects the link's own
// content, and a cross-origin redirect is how a fetch becomes an unexpected
// request to somewhere the author never chose.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (*Page, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("websource: invalid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, ErrUnsupportedScheme
	}
	if parsed.Host == "" {
		return nil, errors.New("websource: URL has no host")
	}
	origin := parsed.Scheme + "://" + parsed.Host

	client := f.cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: f.cfg.Timeout}
	}
	// Copy the client so the redirect policy is this call's, not the caller's.
	clone := *client
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("websource: too many redirects")
		}
		if req.URL.Scheme+"://"+req.URL.Host != origin {
			return fmt.Errorf("websource: refused a cross-origin redirect to %s", req.URL.Host)
		}
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.cfg.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.5")

	resp, err := clone.Do(req)
	if err != nil {
		return nil, fmt.Errorf("websource: fetch failed: %w", err)
	}
	defer resp.Body.Close()

	// Read one byte past the cap so a hit is detectable.
	limited := io.LimitReader(resp.Body, f.cfg.MaxBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("websource: reading body: %w", err)
	}
	truncated := int64(len(body)) > f.cfg.MaxBytes
	if truncated {
		body = body[:f.cfg.MaxBytes]
	}

	contentType := resp.Header.Get("Content-Type")
	page := &Page{
		URL:        resp.Request.URL.String(),
		Truncated:  truncated,
		StatusCode: resp.StatusCode,
	}
	raw := string(body)
	if strings.Contains(contentType, "html") || looksLikeHTML(raw) {
		page.Title = extractTitle(raw)
		page.Text = HTMLToText(raw)
	} else {
		page.Text = strings.TrimSpace(raw)
	}
	return page, nil
}

// SearchResult is one search hit.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"content"`
}

// Search queries the configured provider.
//
// The request shape follows the common `{query, max_results}` JSON contract, so
// a self-hosted or hosted search service that implements it works without
// changes.
func (f *Fetcher) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	if strings.TrimSpace(f.cfg.SearchEndpoint) == "" {
		return nil, ErrNoProvider
	}
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("websource: query is empty")
	}
	if limit <= 0 {
		limit = f.cfg.SearchResults
	}

	payload, err := json.Marshal(map[string]any{"query": query, "max_results": limit})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.cfg.SearchEndpoint, strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if f.cfg.SearchAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+f.cfg.SearchAPIKey)
	}

	client := f.cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: f.cfg.Timeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("websource: search failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("websource: search returned %d: %s", resp.StatusCode, truncate(string(body), 256))
	}
	// Accept both a bare array and an object wrapping one under `results`.
	var results []SearchResult
	if err := json.Unmarshal(body, &results); err != nil {
		var wrapper struct {
			Results []SearchResult `json:"results"`
		}
		if err2 := json.Unmarshal(body, &wrapper); err2 != nil {
			return nil, fmt.Errorf("websource: search response is not valid JSON: %w", err)
		}
		results = wrapper.Results
	}
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

var (
	reScript      = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	reStyle       = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	reComment     = regexp.MustCompile(`(?s)<!--.*?-->`)
	reTitle       = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reBlockBreaks = regexp.MustCompile(`(?i)</?(p|div|br|li|tr|h[1-6]|section|article|header|footer)[^>]*>`)
	reTags        = regexp.MustCompile(`(?s)<[^>]*>`)
	reBlankLines  = regexp.MustCompile(`\n{3,}`)
	reSpaces      = regexp.MustCompile(`[ \t]{2,}`)
)

// HTMLToText reduces HTML to readable text.
//
// Script, style, and comment content is dropped first, because those blocks
// contain code rather than prose and would otherwise dominate the output.
// Block-level tags become newlines so paragraphs survive as paragraphs.
func HTMLToText(raw string) string {
	s := reScript.ReplaceAllString(raw, " ")
	s = reStyle.ReplaceAllString(s, " ")
	s = reComment.ReplaceAllString(s, " ")
	s = reBlockBreaks.ReplaceAllString(s, "\n")
	s = reTags.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = reSpaces.ReplaceAllString(s, " ")

	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return reBlankLines.ReplaceAllString(strings.Join(out, "\n"), "\n\n")
}

func extractTitle(raw string) string {
	if m := reTitle.FindStringSubmatch(raw); len(m) == 2 {
		return strings.TrimSpace(html.UnescapeString(reTags.ReplaceAllString(m[1], "")))
	}
	return ""
}

func looksLikeHTML(s string) bool {
	head := s
	if len(head) > 512 {
		head = head[:512]
	}
	lower := strings.ToLower(head)
	return strings.Contains(lower, "<html") || strings.Contains(lower, "<!doctype") || strings.Contains(lower, "<body")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
