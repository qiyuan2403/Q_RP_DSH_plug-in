package card

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Layout is the on-disk shape of one decomposed card.
//
// A card is a PNG wrapper around JSON, which makes it a terrible thing to
// version: a one-field edit rewrites the whole binary and no diff is readable.
// Decomposing it into text fixes that — Git can then show exactly which field
// changed — while `.original/` keeps the imported file untouched so the
// packager can prove the round trip.
//
//	<workspace>/<slug>/
//	  .original/card.png     the imported file, never written after import
//	  meta.json              identity: name, version, source keyword
//	  character.json         the card document, pretty-printed for diffing
//	  greetings/             first_mes and each alternate greeting, one file each
//	  lorebook/entries.json  character_book entries
//	  assets/                images and other binaries
//	  dist/<slug>.png        the packed card, ready to import elsewhere
type Layout struct {
	Root string
}

// Dir is the card's directory.
func (l Layout) Dir() string { return l.Root }

// OriginalPath is the preserved import.
func (l Layout) OriginalPath() string { return filepath.Join(l.Root, ".original", "card.png") }

// MetaPath is the identity file.
func (l Layout) MetaPath() string { return filepath.Join(l.Root, "meta.json") }

// CharacterPath is the card document.
func (l Layout) CharacterPath() string { return filepath.Join(l.Root, "character.json") }

// GreetingsDir holds one file per greeting.
func (l Layout) GreetingsDir() string { return filepath.Join(l.Root, "greetings") }

// LorebookPath is the world-info entries file.
func (l Layout) LorebookPath() string { return filepath.Join(l.Root, "lorebook", "entries.json") }

// AssetsDir holds images and other binaries.
func (l Layout) AssetsDir() string { return filepath.Join(l.Root, "assets") }

// DistPath is the packed output.
func (l Layout) DistPath() string { return filepath.Join(l.Root, "dist", filepath.Base(l.Root)+".png") }

// Meta is the decomposition's identity record.
type Meta struct {
	// Name is the card's character name.
	Name string `json:"name"`
	// Slug is the directory-safe form of the name.
	Slug string `json:"slug"`
	// Keyword is the chunk keyword the card was imported from (chara or ccv3).
	Keyword string `json:"keyword"`
	// Spec is the declared card spec, when the document carries one.
	Spec string `json:"spec,omitempty"`
	// SpecVersion is the declared spec version.
	SpecVersion string `json:"specVersion,omitempty"`
	// SourceFile is the original file's base name, for provenance.
	SourceFile string `json:"sourceFile,omitempty"`
}

// Slug converts a character name into a directory-safe name.
//
// It is deliberately conservative: path separators, traversal segments, and
// characters that are awkward on Windows are all replaced, because the slug
// becomes a directory name on the user's disk and a card's name is untrusted
// input from the internet.
func Slug(name string) string {
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
			// Keep non-ASCII letters readable rather than dropping them: card
			// names are frequently CJK.
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	if out == "" || out == "." || out == ".." {
		out = "card"
	}
	return out
}

// Decompose writes a card into the layout under root.
//
// The original is copied FIRST and never overwritten afterwards, so the
// workspace always retains a byte-exact record of what was imported.
func Decompose(root string, png *PNG, c *Card, sourceFile string) (Layout, error) {
	name := cardName(c.Doc)
	slug := Slug(name)
	l := Layout{Root: filepath.Join(root, slug)}
	if err := os.MkdirAll(filepath.Join(l.Root, ".original"), 0o755); err != nil {
		return l, err
	}
	for _, dir := range []string{l.GreetingsDir(), filepath.Dir(l.LorebookPath()), l.AssetsDir(), filepath.Dir(l.DistPath())} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return l, err
		}
	}

	if err := os.WriteFile(l.OriginalPath(), png.Bytes(), 0o644); err != nil {
		return l, err
	}

	meta := Meta{
		Name:        name,
		Slug:        slug,
		Keyword:     c.Keyword,
		Spec:        stringField(c.Doc, "spec"),
		SpecVersion: stringField(c.Doc, "spec_version"),
		SourceFile:  filepath.Base(sourceFile),
	}
	if err := writeJSON(l.MetaPath(), meta); err != nil {
		return l, err
	}

	if err := writeJSON(l.CharacterPath(), c.Doc); err != nil {
		return l, err
	}

	if err := l.writeGreetings(c.Doc); err != nil {
		return l, err
	}
	if err := l.writeLorebook(c.Doc); err != nil {
		return l, err
	}
	return l, nil
}

// writeJSON writes pretty-printed JSON with a trailing newline.
//
// Indentation is the point: the file exists to be diffed and read.
func writeJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o644)
}

// dataDoc returns the `data` object of a card, falling back to the document
// itself for V1 cards, whose fields sit at the top level.
func dataDoc(doc map[string]any) map[string]any {
	if d, ok := doc["data"].(map[string]any); ok {
		return d
	}
	return doc
}

func cardName(doc map[string]any) string {
	if n := stringField(dataDoc(doc), "name"); n != "" {
		return n
	}
	return "card"
}

func stringField(doc map[string]any, key string) string {
	if v, ok := doc[key].(string); ok {
		return v
	}
	return ""
}

// writeGreetings splits first_mes and alternate_greetings into one file each.
//
// Separate files are what make a greeting edit a one-line diff instead of a
// rewritten JSON blob.
func (l Layout) writeGreetings(doc map[string]any) error {
	data := dataDoc(doc)
	if first := stringField(data, "first_mes"); first != "" {
		if err := os.WriteFile(filepath.Join(l.GreetingsDir(), "default.md"), []byte(first), 0o644); err != nil {
			return err
		}
	}
	alts, _ := data["alternate_greetings"].([]any)
	for i, a := range alts {
		s, ok := a.(string)
		if !ok {
			continue
		}
		name := fmt.Sprintf("alternate_%d.md", i+1)
		if err := os.WriteFile(filepath.Join(l.GreetingsDir(), name), []byte(s), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// writeLorebook writes the character_book entries, when present.
func (l Layout) writeLorebook(doc map[string]any) error {
	data := dataDoc(doc)
	book, ok := data["character_book"].(map[string]any)
	if !ok {
		return nil
	}
	return writeJSON(l.LorebookPath(), book)
}

// LoadCharacter reads the edited card document back from the workspace.
func (l Layout) LoadCharacter() (map[string]any, error) {
	raw, err := os.ReadFile(l.CharacterPath())
	if err != nil {
		return nil, err
	}
	doc := map[string]any{}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("card: character.json: %w", err)
	}
	return doc, nil
}

// LoadMeta reads the identity record.
func (l Layout) LoadMeta() (Meta, error) {
	var m Meta
	raw, err := os.ReadFile(l.MetaPath())
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(raw, &m)
	return m, err
}

// Pack rebuilds a PNG from the workspace.
//
// It starts from the PRESERVED ORIGINAL rather than the last packed output, so
// every pack is a clean function of (original image, current document) and
// repeated packs cannot accumulate drift.
func (l Layout) Pack() (*PNG, error) {
	original, err := os.ReadFile(l.OriginalPath())
	if err != nil {
		return nil, err
	}
	png, err := ParsePNG(original)
	if err != nil {
		return nil, err
	}
	doc, err := l.LoadCharacter()
	if err != nil {
		return nil, err
	}
	meta, err := l.LoadMeta()
	if err != nil {
		return nil, err
	}
	keyword := meta.Keyword
	if keyword == "" {
		keyword = KeywordChara
	}
	if err := png.SetCard(keyword, doc); err != nil {
		return nil, err
	}
	return png, nil
}

// PackToFile packs the card and writes it to the dist path.
func (l Layout) PackToFile() (string, error) {
	png, err := l.Pack()
	if err != nil {
		return "", err
	}
	out := l.DistPath()
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(out, png.Bytes(), 0o644); err != nil {
		return "", err
	}
	return out, nil
}
