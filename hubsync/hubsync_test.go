package hubsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nameFromPNG is a stand-in for the card package's name reader.
func nameFromPNG(raw []byte) (string, error) {
	if strings.Contains(string(raw), "Name:") {
		idx := strings.Index(string(raw), "Name:")
		rest := string(raw)[idx+len("Name:"):]
		end := strings.IndexAny(rest, "\n\x00")
		if end < 0 {
			end = len(rest)
		}
		return strings.TrimSpace(rest[:end]), nil
	}
	return "", errors.New("no name")
}

// TestIndexLocalFindsCardsAndReadsNames pins the local index: every PNG is
// listed and its name comes from the card payload.
func TestIndexLocalFindsCardsAndReadsNames(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	write("a.png", "PNG Name:Alpha\n")
	write("b.png", "PNG Name:Beta\n")
	write("notes.txt", "not a card")

	c := New(Config{}, nameFromPNG)
	entries, err := c.IndexLocal(dir)
	if err != nil {
		t.Fatalf("IndexLocal: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (the .txt must be skipped)", len(entries))
	}
	// Sorted by name.
	if entries[0].Name != "Alpha" || entries[1].Name != "Beta" {
		t.Fatalf("names = %q, %q", entries[0].Name, entries[1].Name)
	}
	if entries[0].Slug != "alpha" {
		t.Fatalf("Slug = %q", entries[0].Slug)
	}
	if entries[0].Source != "local" {
		t.Fatalf("Source = %q", entries[0].Source)
	}
}

// TestIndexLocalSkipsDerivedDirectories pins that version-control and derived
// directories are not scanned, so a packed output is not listed as a source.
func TestIndexLocalSkipsDerivedDirectories(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{".git", "dist", ".original"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, sub, "x.png"), []byte("PNG Name:Nope\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "real.png"), []byte("PNG Name:Real\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	c := New(Config{}, nameFromPNG)
	entries, err := c.IndexLocal(dir)
	if err != nil {
		t.Fatalf("IndexLocal: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "Real" {
		t.Fatalf("entries = %+v, want only the top-level card", entries)
	}
}

// TestIndexLocalListsUnparsableCardsWithFallbackName pins that a broken card is
// still visible, with its file name as the label: hiding it would make the
// problem invisible instead of fixable.
func TestIndexLocalListsUnparsableCardsWithFallbackName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.png"), []byte("not a card"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	c := New(Config{}, nameFromPNG)
	entries, err := c.IndexLocal(dir)
	if err != nil {
		t.Fatalf("IndexLocal: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Name != "broken" {
		t.Fatalf("Name = %q, want the file name as a fallback", entries[0].Name)
	}
}

// TestPublishWritesAndReplaces pins that publishing is an update: a second
// publish of the same name replaces the file rather than adding a duplicate.
func TestPublishWritesAndReplaces(t *testing.T) {
	dir := t.TempDir()
	c := New(Config{}, nil)

	first, err := c.Publish([]byte("v1"), "My Card", dir)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if filepath.Base(first) != "my-card.png" {
		t.Fatalf("path = %q, want my-card.png", first)
	}
	second, err := c.Publish([]byte("v2"), "My Card", dir)
	if err != nil {
		t.Fatalf("second Publish: %v", err)
	}
	if first != second {
		t.Fatalf("publishing the same card wrote two files: %q and %q", first, second)
	}
	got, _ := os.ReadFile(second)
	if string(got) != "v2" {
		t.Fatalf("content = %q, want the replacement", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("directory has %d files, want 1", len(entries))
	}
}

// TestPublishRejectsEmptyCard pins fail-loud on empty input.
func TestPublishRejectsEmptyCard(t *testing.T) {
	c := New(Config{}, nil)
	if _, err := c.Publish(nil, "x", t.TempDir()); err == nil {
		t.Fatal("an empty card was published")
	}
}

// TestPublishSlugifiesUntrustedName pins path safety: a card name from the
// internet must not escape the destination directory.
func TestPublishSlugifiesUntrustedName(t *testing.T) {
	dir := t.TempDir()
	c := New(Config{}, nil)
	path, err := c.Publish([]byte("x"), "../../etc/passwd", dir)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	if strings.HasPrefix(rel, "..") {
		t.Fatalf("the card escaped the destination: %s", path)
	}
}

// TestListRemoteResolvesRelativePaths pins that a relative entry in a remote
// index becomes an absolute URL under the configured base.
func TestListRemoteResolvesRelativePaths(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"name": "Remote One", "file": "cards/one.png"},
			{"name": "Remote Two", "file": "https://other.example/two.png"},
		})
	}))
	defer srv.Close()

	c := New(Config{RemoteBase: srv.URL}, nil)
	entries, err := c.ListRemote(context.Background(), "/index.json")
	if err != nil {
		t.Fatalf("ListRemote: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].File != srv.URL+"/cards/one.png" {
		t.Fatalf("relative entry = %q, want it resolved under the base", entries[0].File)
	}
	if entries[1].File != "https://other.example/two.png" {
		t.Fatalf("absolute entry was rewritten: %q", entries[1].File)
	}
	if entries[0].Source != "remote" {
		t.Fatalf("Source = %q", entries[0].Source)
	}
}

// TestDownloadResolvesRelativeAndAbsolute pins both download forms.
func TestDownloadResolvesRelativeAndAbsolute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("card-bytes:" + r.URL.Path))
	}))
	defer srv.Close()

	c := New(Config{RemoteBase: srv.URL}, nil)
	rel, err := c.Download(context.Background(), "cards/a.png")
	if err != nil {
		t.Fatalf("Download(relative): %v", err)
	}
	if !strings.Contains(string(rel), "/cards/a.png") {
		t.Fatalf("relative download = %q", rel)
	}
	abs, err := c.Download(context.Background(), srv.URL+"/direct.png")
	if err != nil {
		t.Fatalf("Download(absolute): %v", err)
	}
	if !strings.Contains(string(abs), "/direct.png") {
		t.Fatalf("absolute download = %q", abs)
	}
}

// TestRemoteRequiresBase pins fail-loud with no configured base.
func TestRemoteRequiresBase(t *testing.T) {
	c := New(Config{}, nil)
	if _, err := c.ListRemote(context.Background(), "/index.json"); !errors.Is(err, ErrNoRemote) {
		t.Fatalf("error = %v, want ErrNoRemote", err)
	}
	if _, err := c.Download(context.Background(), "cards/a.png"); !errors.Is(err, ErrNoRemote) {
		t.Fatalf("error = %v, want ErrNoRemote", err)
	}
}

// TestRemoteSurfacesHTTPError pins that a non-200 is reported with its status.
func TestRemoteSurfacesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := New(Config{RemoteBase: srv.URL}, nil)
	_, err := c.Download(context.Background(), "missing.png")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error = %v, want it to carry the status", err)
	}
}

// TestListRemoteRejectsNonJSON pins that a malformed index is refused rather
// than treated as empty.
func TestListRemoteRejectsNonJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not json</html>"))
	}))
	defer srv.Close()
	c := New(Config{RemoteBase: srv.URL}, nil)
	if _, err := c.ListRemote(context.Background(), "/index.json"); err == nil {
		t.Fatal("a non-JSON index was accepted")
	}
}
