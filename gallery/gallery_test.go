package gallery

import (
	"os"
	"path/filepath"
	"testing"
)

// writePNG writes a tiny file with a PNG signature so the extension and content
// both look like an image.
func writePNG(t *testing.T, path string, seed byte) {
	t.Helper()
	data := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', seed}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestAddCopiesAndIndexes pins the basic add path and that the index persists.
func TestAddCopiesAndIndexes(t *testing.T) {
	root := t.TempDir()
	g, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	src := filepath.Join(t.TempDir(), "Art.png")
	writePNG(t, src, 1)

	item, err := g.Add(src, "Art", []string{"expression"}, "happy")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if item.File != "art.png" {
		t.Fatalf("File = %q, want art.png", item.File)
	}
	if item.Emotion != "happy" {
		t.Fatalf("Emotion = %q", item.Emotion)
	}
	if _, err := os.Stat(g.Path(item)); err != nil {
		t.Fatalf("the added file is missing: %v", err)
	}

	idx, err := g.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(idx.Items) != 1 {
		t.Fatalf("index has %d items, want 1", len(idx.Items))
	}
}

// TestAddRefusesNonImage pins that the gallery does not become a general file
// store.
func TestAddRefusesNonImage(t *testing.T) {
	g, _ := New(t.TempDir())
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := g.Add(src, "notes", nil, ""); err == nil {
		t.Fatal("a non-image file was accepted")
	}
}

// TestAddAvoidsClobbering pins that a second add of the same name does not
// overwrite the first file.
func TestAddAvoidsClobbering(t *testing.T) {
	g, _ := New(t.TempDir())
	dir := t.TempDir()
	a := filepath.Join(dir, "a.png")
	b := filepath.Join(dir, "b.png")
	writePNG(t, a, 1)
	writePNG(t, b, 2)

	first, err := g.Add(a, "Same", nil, "")
	if err != nil {
		t.Fatalf("Add(a): %v", err)
	}
	second, err := g.Add(b, "Same", nil, "")
	if err != nil {
		t.Fatalf("Add(b): %v", err)
	}
	if first.File == second.File {
		t.Fatalf("both images landed at %q", first.File)
	}
	idx, _ := g.Load()
	if len(idx.Items) != 2 {
		t.Fatalf("index has %d items, want 2", len(idx.Items))
	}
}

// TestScanPreservesAuthoredMetadata pins the property that makes a rescan safe:
// names, tags, and emotion are authored data and must survive.
func TestScanPreservesAuthoredMetadata(t *testing.T) {
	root := t.TempDir()
	g, _ := New(root)
	writePNG(t, filepath.Join(root, "hero.png"), 7)
	idx, err := g.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(idx.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(idx.Items))
	}
	idx.Items[0].Tags = []string{"main"}
	idx.Items[0].Emotion = "neutral"
	idx.Items[0].Name = "The Hero"
	if err := g.Save(idx); err != nil {
		t.Fatalf("Save: %v", err)
	}

	again, err := g.Scan()
	if err != nil {
		t.Fatalf("second Scan: %v", err)
	}
	got := again.Items[0]
	if got.Name != "The Hero" {
		t.Fatalf("Name = %q, want the authored value", got.Name)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "main" {
		t.Fatalf("Tags = %v, want [main]", got.Tags)
	}
	if got.Emotion != "neutral" {
		t.Fatalf("Emotion = %q", got.Emotion)
	}
}

// TestScanFollowsContentAcrossRename pins that a renamed file keeps its
// authored metadata, matched by content hash rather than by path.
func TestScanFollowsContentAcrossRename(t *testing.T) {
	root := t.TempDir()
	g, _ := New(root)
	writePNG(t, filepath.Join(root, "before.png"), 3)
	idx, _ := g.Scan()
	idx.Items[0].Tags = []string{"kept"}
	idx.Items[0].Emotion = "smile"
	_ = g.Save(idx)

	if err := os.Rename(filepath.Join(root, "before.png"), filepath.Join(root, "after.png")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	again, err := g.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(again.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(again.Items))
	}
	if again.Items[0].File != "after.png" {
		t.Fatalf("File = %q, want after.png", again.Items[0].File)
	}
	if len(again.Items[0].Tags) != 1 || again.Items[0].Tags[0] != "kept" {
		t.Fatalf("metadata lost across rename: %v", again.Items[0])
	}
	if again.Items[0].Emotion != "smile" {
		t.Fatalf("Emotion = %q, want smile", again.Items[0].Emotion)
	}
}

// TestScanDropsDeletedFiles pins that a removed image leaves the index.
func TestScanDropsDeletedFiles(t *testing.T) {
	root := t.TempDir()
	g, _ := New(root)
	writePNG(t, filepath.Join(root, "gone.png"), 1)
	writePNG(t, filepath.Join(root, "stays.png"), 2)
	if _, err := g.Scan(); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "gone.png")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	again, err := g.Scan()
	if err != nil {
		t.Fatalf("second Scan: %v", err)
	}
	if len(again.Items) != 1 || again.Items[0].File != "stays.png" {
		t.Fatalf("items = %+v, want only stays.png", again.Items)
	}
}

// TestFindByTag pins tag filtering.
func TestFindByTag(t *testing.T) {
	root := t.TempDir()
	g, _ := New(root)
	writePNG(t, filepath.Join(root, "a.png"), 1)
	writePNG(t, filepath.Join(root, "b.png"), 2)
	idx, _ := g.Scan()
	idx.Items[0].Tags = []string{"outfit"}
	_ = g.Save(idx)

	found, err := g.Find("outfit")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("Find returned %d items, want 1", len(found))
	}
	none, err := g.Find("absent")
	if err != nil {
		t.Fatalf("Find(absent): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("Find(absent) returned %d items", len(none))
	}
}

// TestAddSlugifiesUntrustedNames pins path safety: an asset name from a
// downloaded card must not escape the gallery directory.
func TestAddSlugifiesUntrustedNames(t *testing.T) {
	root := t.TempDir()
	g, _ := New(root)
	src := filepath.Join(t.TempDir(), "x.png")
	writePNG(t, src, 1)

	item, err := g.Add(src, "../../escape", nil, "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if item.File != "escape.png" {
		t.Fatalf("File = %q, want escape.png", item.File)
	}
	abs := g.Path(item)
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	if rel == ".." || len(rel) > 2 && rel[:2] == ".." {
		t.Fatalf("the file escaped the gallery root: %s", abs)
	}
}

// TestLoadMissingIndexIsEmpty pins that a fresh gallery loads without error.
func TestLoadMissingIndexIsEmpty(t *testing.T) {
	g, _ := New(t.TempDir())
	idx, err := g.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(idx.Items) != 0 {
		t.Fatalf("fresh index has %d items", len(idx.Items))
	}
}

// TestSaveSortsForStableDiffs pins that the index file is stable, so committing
// it produces a minimal diff.
func TestSaveSortsForStableDiffs(t *testing.T) {
	g, _ := New(t.TempDir())
	idx := &Index{Items: []Item{{File: "z.png"}, {File: "a.png"}, {File: "m.png"}}}
	if err := g.Save(idx); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, _ := g.Load()
	want := []string{"a.png", "m.png", "z.png"}
	for i, f := range want {
		if reloaded.Items[i].File != f {
			t.Fatalf("order = %v, want %v", []string{reloaded.Items[0].File, reloaded.Items[1].File, reloaded.Items[2].File}, want)
		}
	}
}
