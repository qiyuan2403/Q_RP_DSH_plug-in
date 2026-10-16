package websource

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestFetchReducesHTMLToText pins the reduction: script and style content is
// dropped, block tags become paragraph breaks, entities are decoded.
func TestFetchReducesHTMLToText(t *testing.T) {
	page := `<!doctype html>
<html><head><title>Card Guide</title>
<style>body { color: red; }</style>
<script>var secret = "should not appear";</script>
</head><body>
<h1>Heading</h1>
<p>First &amp; foremost</p>
<div>Second</div>
</body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	f := New(Config{})
	got, err := f.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Title != "Card Guide" {
		t.Fatalf("Title = %q", got.Title)
	}
	if strings.Contains(got.Text, "should not appear") {
		t.Fatalf("script content leaked into the text:\n%s", got.Text)
	}
	if strings.Contains(got.Text, "color: red") {
		t.Fatalf("style content leaked into the text:\n%s", got.Text)
	}
	if !strings.Contains(got.Text, "First & foremost") {
		t.Fatalf("entity was not decoded:\n%s", got.Text)
	}
	// Paragraph structure must survive.
	if !strings.Contains(got.Text, "Heading\n") {
		t.Fatalf("block tags did not become breaks:\n%q", got.Text)
	}
}

// TestFetchRefusesNonHTTPScheme pins the scheme allowlist.
func TestFetchRefusesNonHTTPScheme(t *testing.T) {
	f := New(Config{})
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com/x", "javascript:alert(1)"} {
		if _, err := f.Fetch(context.Background(), u); !errors.Is(err, ErrUnsupportedScheme) {
			t.Fatalf("Fetch(%q) error = %v, want ErrUnsupportedScheme", u, err)
		}
	}
}

// TestFetchRefusesCrossOriginRedirect pins the redirect guard: a redirect off
// the original host is refused, because following it would request somewhere
// the author never chose.
func TestFetchRefusesCrossOriginRedirect(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("elsewhere"))
	}))
	defer other.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer origin.Close()

	f := New(Config{})
	_, err := f.Fetch(context.Background(), origin.URL)
	if err == nil {
		t.Fatal("a cross-origin redirect was followed")
	}
	if !strings.Contains(err.Error(), "cross-origin") {
		t.Fatalf("error = %v, want it to name the cross-origin refusal", err)
	}
}

// TestFetchFollowsSameOriginRedirect pins that a legitimate same-host redirect
// still works.
func TestFetchFollowsSameOriginRedirect(t *testing.T) {
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("arrived"))
			return
		}
		http.Redirect(w, r, base+"/final", http.StatusFound)
	}))
	defer srv.Close()
	base = srv.URL

	f := New(Config{})
	got, err := f.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(got.Text, "arrived") {
		t.Fatalf("Text = %q", got.Text)
	}
}

// TestFetchCapsBodySize pins the size cap and the truncation flag.
func TestFetchCapsBodySize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("x", 10000)))
	}))
	defer srv.Close()

	f := New(Config{MaxBytes: 100})
	got, err := f.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !got.Truncated {
		t.Fatal("Truncated is false for an oversized body")
	}
	if len(got.Text) > 100 {
		t.Fatalf("Text is %d bytes, want at most 100", len(got.Text))
	}
}

// TestFetchRejectsMissingHost pins the URL validation.
func TestFetchRejectsMissingHost(t *testing.T) {
	f := New(Config{})
	if _, err := f.Fetch(context.Background(), "http:///nohost"); err == nil {
		t.Fatal("a URL with no host was accepted")
	}
}

// TestFetchRespectsTimeout pins that a slow server does not hang the caller.
func TestFetchRespectsTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer func() { close(release); srv.Close() }()

	f := New(Config{Timeout: 100 * time.Millisecond})
	start := time.Now()
	if _, err := f.Fetch(context.Background(), srv.URL); err == nil {
		t.Fatal("a hung request did not time out")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
}

// TestSearchPostsQueryAndParsesResults pins the search contract.
func TestSearchPostsQueryAndParsesResults(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"title": "A", "url": "https://a.example", "content": "about a"},
			{"title": "B", "url": "https://b.example", "content": "about b"},
		})
	}))
	defer srv.Close()

	f := New(Config{SearchEndpoint: srv.URL})
	results, err := f.Search(context.Background(), "card lore", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotBody["query"] != "card lore" {
		t.Fatalf("query on the wire = %v", gotBody["query"])
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Title != "A" {
		t.Fatalf("Title = %q", results[0].Title)
	}
}

// TestSearchAcceptsWrappedResults pins that an object wrapping the array under
// `results` is also accepted, since providers differ.
func TestSearchAcceptsWrappedResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"title": "Wrapped", "url": "https://x.example"}},
		})
	}))
	defer srv.Close()

	f := New(Config{SearchEndpoint: srv.URL})
	results, err := f.Search(context.Background(), "q", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Wrapped" {
		t.Fatalf("results = %+v", results)
	}
}

// TestSearchHonorsLimit pins the result cap.
func TestSearchHonorsLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var many []map[string]any
		for i := range 10 {
			many = append(many, map[string]any{"title": "t", "url": "u"})
			_ = i
		}
		_ = json.NewEncoder(w).Encode(many)
	}))
	defer srv.Close()

	f := New(Config{SearchEndpoint: srv.URL})
	results, err := f.Search(context.Background(), "q", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
}

// TestSearchRequiresProvider pins fail-loud with no endpoint.
func TestSearchRequiresProvider(t *testing.T) {
	f := New(Config{})
	if _, err := f.Search(context.Background(), "q", 1); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("error = %v, want ErrNoProvider", err)
	}
}

// TestSearchRequiresQuery pins fail-loud on an empty query.
func TestSearchRequiresQuery(t *testing.T) {
	f := New(Config{SearchEndpoint: "http://example.invalid"})
	if _, err := f.Search(context.Background(), "  ", 1); err == nil {
		t.Fatal("an empty query was accepted")
	}
}

// TestHTMLToTextHandlesPlainText pins that non-HTML input passes through.
func TestHTMLToTextHandlesPlainText(t *testing.T) {
	got := HTMLToText("just text\nwith lines")
	if !strings.Contains(got, "just text") {
		t.Fatalf("Text = %q", got)
	}
}
