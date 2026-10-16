// Package gallery indexes the images that belong to a card.
//
// A card's images live in two places: the avatar embedded in the PNG, and any
// extra art the author adds. The gallery keeps an index of the extras so the
// workshop can list, tag, and select them without rescanning a directory on
// every request.
//
// The index is a plain JSON file inside the card's assets directory, which
// keeps it diffable and lets the user edit it by hand.
package gallery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Image extensions the gallery recognizes. The set is deliberately small and
// explicit: a card's assets are images, and accepting arbitrary files would
// turn the index into a general-purpose file list.
var imageExtensions = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".webp": true,
	".gif":  true,
	".avif": true,
}

// IndexFilename is the index file's name inside the assets directory.
const IndexFilename = "gallery.json"

// Item is one image in the gallery.
type Item struct {
	// File is the path relative to the assets directory, using forward slashes
	// so an index stays portable across platforms.
	File string `json:"file"`
	// Name is a human label, defaulting to the file's base name.
	Name string `json:"name,omitempty"`
	// Tags are free-form labels for filtering (expression, outfit, scene).
	Tags []string `json:"tags,omitempty"`
	// Size is the file's byte length at index time.
	Size int64 `json:"size"`
	// SHA256 is the content hash, so a moved or renamed file can be recognized
	// rather than re-added.
	SHA256 string `json:"sha256"`
	// Emotion marks an expression/differential image, which the workshop can
	// offer as a per-message variant.
	Emotion string `json:"emotion,omitempty"`
}

// Index is the on-disk gallery.
type Index struct {
	Items []Item `json:"items"`
}

// Dir is a gallery rooted at an assets directory.
type Dir struct {
	Root string
}

// New returns a gallery for an assets directory, creating it when needed.
func New(root string) (*Dir, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("gallery: %w", err)
	}
	return &Dir{Root: root}, nil
}

// indexPath is the index file's path.
func (d *Dir) indexPath() string { return filepath.Join(d.Root, IndexFilename) }

// Load reads the index, returning an empty one when the file does not exist.
func (d *Dir) Load() (*Index, error) {
	raw, err := os.ReadFile(d.indexPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &Index{}, nil
		}
		return nil, err
	}
	idx := &Index{}
	if err := json.Unmarshal(raw, idx); err != nil {
		return nil, fmt.Errorf("gallery: index is not valid JSON: %w", err)
	}
	return idx, nil
}

// Save writes the index, pretty-printed so it stays diffable.
func (d *Dir) Save(idx *Index) error {
	sort.Slice(idx.Items, func(i, j int) bool { return idx.Items[i].File < idx.Items[j].File })
	raw, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(d.indexPath(), raw, 0o644)
}

// Scan rebuilds the index from the directory contents.
//
// Existing entries keep their name, tags, and emotion: those are authored data
// that a rescan must not discard. A file that disappeared is dropped; a new
// file is added.
func (d *Dir) Scan() (*Index, error) {
	previous, err := d.Load()
	if err != nil {
		return nil, err
	}
	byHash := map[string]Item{}
	byFile := map[string]Item{}
	for _, it := range previous.Items {
		byHash[it.SHA256] = it
		byFile[it.File] = it
	}

	var items []Item
	err = filepath.WalkDir(d.Root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !imageExtensions[strings.ToLower(filepath.Ext(entry.Name()))] {
			return nil
		}
		rel, err := filepath.Rel(d.Root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		info, err := entry.Info()
		if err != nil {
			return err
		}
		sum, err := hashFile(path)
		if err != nil {
			return err
		}

		item := Item{
			File:   rel,
			Name:   strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())),
			Size:   info.Size(),
			SHA256: sum,
		}
		// Carry authored metadata across a rename by matching content, then
		// across a same-path rescan.
		if old, ok := byFile[rel]; ok {
			item.Name = firstNonEmpty(old.Name, item.Name)
			item.Tags = old.Tags
			item.Emotion = old.Emotion
		} else if old, ok := byHash[sum]; ok {
			item.Name = firstNonEmpty(old.Name, item.Name)
			item.Tags = old.Tags
			item.Emotion = old.Emotion
		}
		items = append(items, item)
		return nil
	})
	if err != nil {
		return nil, err
	}
	idx := &Index{Items: items}
	if err := d.Save(idx); err != nil {
		return nil, err
	}
	return idx, nil
}

// Add registers a file by copying it into the gallery.
//
// The name is slugified so an untrusted card cannot plant a path traversal.
func (d *Dir) Add(source, name string, tags []string, emotion string) (Item, error) {
	raw, err := os.ReadFile(source)
	if err != nil {
		return Item{}, err
	}
	ext := strings.ToLower(filepath.Ext(source))
	if !imageExtensions[ext] {
		return Item{}, fmt.Errorf("gallery: %q is not a recognized image type", ext)
	}
	base := slug(name)
	if base == "" {
		base = slug(strings.TrimSuffix(filepath.Base(source), filepath.Ext(source)))
	}
	if base == "" {
		base = "image"
	}
	rel := base + ext
	// Avoid clobbering an unrelated file that happens to share the name.
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(d.Root, rel)); os.IsNotExist(err) {
			break
		}
		rel = fmt.Sprintf("%s-%d%s", base, i, ext)
	}
	if err := os.WriteFile(filepath.Join(d.Root, rel), raw, 0o644); err != nil {
		return Item{}, err
	}
	sum := sha256.Sum256(raw)
	item := Item{
		File:    rel,
		Name:    strings.TrimSuffix(rel, ext),
		Tags:    tags,
		Size:    int64(len(raw)),
		SHA256:  hex.EncodeToString(sum[:]),
		Emotion: emotion,
	}
	idx, err := d.Load()
	if err != nil {
		return Item{}, err
	}
	idx.Items = append(idx.Items, item)
	if err := d.Save(idx); err != nil {
		return Item{}, err
	}
	return item, nil
}

// Find returns the items carrying a tag.
func (d *Dir) Find(tag string) ([]Item, error) {
	idx, err := d.Load()
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, it := range idx.Items {
		for _, t := range it.Tags {
			if t == tag {
				out = append(out, it)
				break
			}
		}
	}
	return out, nil
}

// Path returns an item's absolute path.
func (d *Dir) Path(it Item) string {
	return filepath.Join(d.Root, filepath.FromSlash(it.File))
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// slug produces a filesystem-safe base name, dropping path separators and
// traversal segments because asset names are untrusted input.
func slug(s string) string {
	var b strings.Builder
	for _, r := range s {
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
