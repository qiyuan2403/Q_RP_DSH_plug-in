// Package hubsync exchanges cards with an RP-Hub deployment.
//
// RP-Hub is a browser application whose data lives in local storage, and whose
// distributable artifacts are the card PNGs themselves. There is no server-side
// card API to call, so this package works with what a deployment actually
// exposes:
//
//   - A card is exchanged as a PNG file, read and written through the card
//     package, which is aligned to RP-Hub's own reader and writer.
//   - A directory of cards can be indexed, which is how a self-hosted
//     collection (the pattern RP-Hub's own `character/` folder uses) is listed
//     and searched.
//   - A remote base URL can be listed and downloaded, for deployments that
//     publish cards over HTTP.
//
// Nothing here invents a wire protocol RP-Hub does not speak.
package hubsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrNoRemote reports a remote call with no configured base URL.
var ErrNoRemote = errors.New("hubsync: no remote base URL configured")

// Entry is one card found locally or remotely.
type Entry struct {
	// Name is the card's character name, read from its payload.
	Name string `json:"name"`
	// File is the local path, or the remote URL.
	File string `json:"file"`
	// Slug is the directory-safe form of the name.
	Slug string `json:"slug"`
	// Size is the PNG's byte length.
	Size int64 `json:"size"`
	// Modified is the file's modification time, when known.
	Modified string `json:"modified,omitempty"`
	// Source is "local" or "remote".
	Source string `json:"source"`
}

// Config configures the client.
type Config struct {
	// RemoteBase is an RP-Hub deployment base URL. Empty disables remote
	// operations.
	RemoteBase string
	// Timeout bounds one remote request.
	Timeout time.Duration
	// MaxBytes caps a downloaded card.
	MaxBytes int64
	// HTTPClient overrides the client.
	HTTPClient *http.Client
}

// Client lists, downloads, and publishes cards.
type Client struct {
	cfg Config
	// cardName reads a card's character name from PNG bytes. It is injected so
	// this package does not depend on the card package directly, which keeps
	// the two plugins independently usable.
	cardName func([]byte) (string, error)
}

// New builds a client. cardName may be nil, in which case names fall back to
// file names.
func New(cfg Config, cardName func([]byte) (string, error)) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = 64 << 20
	}
	return &Client{cfg: cfg, cardName: cardName}
}

// IndexLocal walks a directory and returns every card PNG it contains.
//
// A card that cannot be parsed is still listed, with its file name as the
// display name: hiding it would make a broken card invisible instead of
// visible-and-fixable.
func (c *Client) IndexLocal(dir string) ([]Entry, error) {
	var out []Entry
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			// Skip the version-control and derived directories, which hold no
			// source cards.
			base := d.Name()
			if base == ".git" || base == "dist" || base == ".original" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".png") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entry := Entry{
			File:     path,
			Slug:     slugOf(strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))),
			Size:     info.Size(),
			Modified: info.ModTime().UTC().Format(time.RFC3339),
			Source:   "local",
		}
		if raw, err := os.ReadFile(path); err == nil && c.cardName != nil {
			if name, err := c.cardName(raw); err == nil && name != "" {
				entry.Name = name
				entry.Slug = slugOf(name)
			}
		}
		if entry.Name == "" {
			entry.Name = strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
		}
		out = append(out, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Publish copies a card PNG into a directory, naming it after the character.
//
// The destination is created when missing, and an existing card of the same
// name is replaced: publishing is an update, not an append.
func (c *Client) Publish(png []byte, name, dir string) (string, error) {
	if len(png) == 0 {
		return "", errors.New("hubsync: no card bytes to publish")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	base := slugOf(name)
	if base == "" {
		base = "card"
	}
	path := filepath.Join(dir, base+".png")
	if err := os.WriteFile(path, png, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// ListRemote asks a deployment for its card index.
//
// The expected document is a JSON array of entries, which is the shape a
// deployment serving its `character/` directory produces.
func (c *Client) ListRemote(ctx context.Context, indexPath string) ([]Entry, error) {
	if strings.TrimSpace(c.cfg.RemoteBase) == "" {
		return nil, ErrNoRemote
	}
	if indexPath == "" {
		indexPath = "/index.json"
	}
	endpoint, err := c.resolve(indexPath)
	if err != nil {
		return nil, err
	}
	body, err := c.get(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	var entries []Entry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("hubsync: remote index is not valid JSON: %w", err)
	}
	for i := range entries {
		entries[i].Source = "remote"
		if !strings.HasPrefix(entries[i].File, "http") {
			entries[i].File, _ = c.resolve(entries[i].File)
		}
	}
	return entries, nil
}

// Download fetches one card.
func (c *Client) Download(ctx context.Context, ref string) ([]byte, error) {
	target := ref
	if !strings.HasPrefix(ref, "http://") && !strings.HasPrefix(ref, "https://") {
		if strings.TrimSpace(c.cfg.RemoteBase) == "" {
			return nil, ErrNoRemote
		}
		resolved, err := c.resolve(ref)
		if err != nil {
			return nil, err
		}
		target = resolved
	}
	return c.get(ctx, target)
}

// resolve turns a possibly relative path into an absolute URL under the
// configured base.
func (c *Client) resolve(ref string) (string, error) {
	base, err := url.Parse(c.cfg.RemoteBase)
	if err != nil {
		return "", fmt.Errorf("hubsync: invalid remote base: %w", err)
	}
	rel, err := url.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("hubsync: invalid reference %q: %w", ref, err)
	}
	return base.ResolveReference(rel).String(), nil
}

func (c *Client) get(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	client := c.cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: c.cfg.Timeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hubsync: request to %s failed: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hubsync: %s returned %d", endpoint, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.cfg.MaxBytes))
	if err != nil {
		return nil, err
	}
	return body, nil
}

// slugOf produces a directory-safe name. Card names are untrusted input, so
// path separators and traversal segments are dropped.
func slugOf(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r == ' ' || r == '-' || r == '_':
			b.WriteRune('-')
		case r > 127:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	if out == "." || out == ".." {
		return ""
	}
	return out
}
