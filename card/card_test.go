package card

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildFixture makes a minimal valid PNG carrying a card payload.
func buildFixture(t *testing.T, keyword string, doc map[string]any) []byte {
	t.Helper()
	png := &PNG{Chunks: []Chunk{
		{Type: "IHDR", Data: []byte{0, 0, 0, 1, 0, 0, 0, 1, 8, 2, 0, 0, 0}},
		{Type: "IDAT", Data: []byte{0x78, 0x9c, 0x63, 0x00, 0x00, 0x00, 0x01, 0x00, 0x01}},
		{Type: "IEND", Data: nil},
	}}
	if err := png.SetCard(keyword, doc); err != nil {
		t.Fatalf("SetCard: %v", err)
	}
	return png.Bytes()
}

func sampleCard() map[string]any {
	return map[string]any{
		"spec":         "chara_card_v2",
		"spec_version": "2.0",
		"data": map[string]any{
			"name":                "TestChar",
			"description":         "A test character",
			"first_mes":           "Hello {{user}}!",
			"alternate_greetings": []any{"Alt one", "Alt two"},
			"character_book": map[string]any{
				"name":    "book",
				"entries": []any{map[string]any{"keys": []any{"lab"}, "content": "A lab."}},
			},
		},
	}
}

// TestRoundTripPreservesImageChunks pins losslessness: every chunk that is not
// the card payload must survive a decompose/pack cycle byte-for-byte, or
// editing a card would silently rewrite the user's artwork metadata.
func TestRoundTripPreservesImageChunks(t *testing.T) {
	original := buildFixture(t, KeywordChara, sampleCard())
	png, c, err := ParsePNGAndCard(original)
	if err != nil {
		t.Fatalf("ParsePNGAndCard: %v", err)
	}
	if c.Keyword != KeywordChara {
		t.Fatalf("keyword = %q, want %q", c.Keyword, KeywordChara)
	}

	root := t.TempDir()
	l, err := Decompose(root, png, c, "TestChar.png")
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	packed, err := l.Pack()
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}

	reparsed, err := ParsePNG(packed.Bytes())
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	// IHDR and IDAT must be identical.
	want := map[string][]byte{}
	for _, ch := range png.Chunks {
		if ch.Type == "IHDR" || ch.Type == "IDAT" {
			want[ch.Type] = ch.Data
		}
	}
	for _, ch := range reparsed.Chunks {
		if w, ok := want[ch.Type]; ok && string(w) != string(ch.Data) {
			t.Fatalf("chunk %s changed across the round trip", ch.Type)
		}
	}
}

// TestRoundTripPreservesCardFields pins that a decompose/pack cycle with no
// edit yields the same document.
func TestRoundTripPreservesCardFields(t *testing.T) {
	original := buildFixture(t, KeywordChara, sampleCard())
	png, c, err := ParsePNGAndCard(original)
	if err != nil {
		t.Fatalf("ParsePNGAndCard: %v", err)
	}
	root := t.TempDir()
	l, err := Decompose(root, png, c, "TestChar.png")
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	packed, err := l.Pack()
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	_, c2, err := ParsePNGAndCard(packed.Bytes())
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}

	// Compare the flattened scalar leaves rather than raw bytes: JSON number
	// formatting may legitimately differ, the values may not.
	got := flatten(c2.Doc)
	want := flatten(c.Doc)
	if len(got) != len(want) {
		t.Fatalf("field count %d != %d\ngot=%v\nwant=%v", len(got), len(want), got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("field %s = %v, want %v", k, got[k], v)
		}
	}
}

// TestEditPropagatesToPackedCard pins the actual workflow: edit the decomposed
// JSON, pack, and observe the change in the rebuilt card.
func TestEditPropagatesToPackedCard(t *testing.T) {
	original := buildFixture(t, KeywordChara, sampleCard())
	png, c, err := ParsePNGAndCard(original)
	if err != nil {
		t.Fatalf("ParsePNGAndCard: %v", err)
	}
	root := t.TempDir()
	l, err := Decompose(root, png, c, "TestChar.png")
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}

	doc, err := l.LoadCharacter()
	if err != nil {
		t.Fatalf("LoadCharacter: %v", err)
	}
	data := doc["data"].(map[string]any)
	data["description"] = "REWRITTEN by the test"
	if err := writeJSON(l.CharacterPath(), doc); err != nil {
		t.Fatalf("write character: %v", err)
	}

	out, err := l.PackToFile()
	if err != nil {
		t.Fatalf("PackToFile: %v", err)
	}
	_, c2, err := ParsePNGAndCard(mustRead(t, out))
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	got := c2.Doc["data"].(map[string]any)["description"]
	if got != "REWRITTEN by the test" {
		t.Fatalf("description = %v, want the edit", got)
	}
}

// TestOriginalIsPreservedAndPackingIsIdempotent pins the two guarantees that
// make repeated packing safe: the import is never overwritten, and packing
// twice from an unchanged workspace yields identical bytes.
func TestOriginalIsPreservedAndPackingIsIdempotent(t *testing.T) {
	original := buildFixture(t, KeywordChara, sampleCard())
	png, c, err := ParsePNGAndCard(original)
	if err != nil {
		t.Fatalf("ParsePNGAndCard: %v", err)
	}
	root := t.TempDir()
	l, err := Decompose(root, png, c, "TestChar.png")
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	before := mustRead(t, l.OriginalPath())

	first, err := l.PackToFile()
	if err != nil {
		t.Fatalf("first pack: %v", err)
	}
	firstBytes := mustRead(t, first)
	second, err := l.PackToFile()
	if err != nil {
		t.Fatalf("second pack: %v", err)
	}
	secondBytes := mustRead(t, second)
	if string(firstBytes) != string(secondBytes) {
		t.Fatal("packing twice produced different bytes")
	}
	if string(before) != string(mustRead(t, l.OriginalPath())) {
		t.Fatal("the preserved original was modified by packing")
	}
}

// TestCharaWinsOverCCV3 pins reader precedence. RP-Hub reads `chara` FIRST and
// only falls back to `ccv3`, so a card carrying both must be read as its
// `chara` payload — the opposite of the general-purpose convention, and the
// reason this package is aligned to RP-Hub rather than to a spec.
func TestCharaWinsOverCCV3(t *testing.T) {
	v2 := sampleCard()
	v3 := sampleCard()
	v3["spec"] = "chara_card_v3"
	v3["data"].(map[string]any)["name"] = "CCV3Name"

	png := &PNG{Chunks: []Chunk{
		{Type: "IHDR", Data: []byte{0, 0, 0, 1, 0, 0, 0, 1, 8, 2, 0, 0, 0}},
		{Type: "IEND", Data: nil},
	}}
	if err := png.SetCard(KeywordChara, v2); err != nil {
		t.Fatalf("SetCard v2: %v", err)
	}
	// Insert v3 manually so BOTH chunks coexist.
	raw, _ := json.Marshal(v3)
	chunk := makeTextChunk(KeywordCCV3, base64Encode(raw))
	png.Chunks = append(png.Chunks[:1], append([]Chunk{chunk}, png.Chunks[1:]...)...)

	_, c, err := ParsePNGAndCard(png.Bytes())
	if err != nil {
		t.Fatalf("ParsePNGAndCard: %v", err)
	}
	if c.Keyword != KeywordChara {
		t.Fatalf("keyword = %q, want %q to win", c.Keyword, KeywordChara)
	}
	if name := c.Doc["data"].(map[string]any)["name"]; name != "TestChar" {
		t.Fatalf("name = %v, want the chara payload's TestChar", name)
	}
}

// TestCorruptCRCDetected pins that a mangled card is rejected at import rather
// than written back as a subtly broken file.
func TestCorruptCRCDetected(t *testing.T) {
	good := buildFixture(t, KeywordChara, sampleCard())
	bad := append([]byte(nil), good...)
	// Flip a byte inside the IHDR payload.
	bad[20] ^= 0xff
	if _, err := ParsePNG(bad); err == nil {
		t.Fatal("a CRC mismatch was accepted")
	} else if !strings.Contains(err.Error(), "CRC") {
		t.Fatalf("error = %v, want it to name the CRC", err)
	}
}

// TestNonPNGRejected pins the format gate.
func TestNonPNGRejected(t *testing.T) {
	if _, err := ParsePNG([]byte("not a png at all")); err == nil {
		t.Fatal("a non-PNG was accepted")
	}
}

// TestPNGWithoutCardChunkRejected pins the missing-payload case.
func TestPNGWithoutCardChunkRejected(t *testing.T) {
	png := &PNG{Chunks: []Chunk{
		{Type: "IHDR", Data: []byte{0, 0, 0, 1, 0, 0, 0, 1, 8, 2, 0, 0, 0}},
		{Type: "IEND", Data: nil},
	}}
	if _, _, err := ParsePNGAndCard(png.Bytes()); err == nil {
		t.Fatal("a PNG without a card chunk was accepted")
	}
}

// TestSlugIsPathSafe pins that an untrusted card name cannot escape the
// workspace: the slug becomes a directory name, and names come from downloads.
func TestSlugIsPathSafe(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd": "etcpasswd",
		"a/b\\c":           "abc",
		"Hello World":      "hello-world",
		"..":               "card",
		"":                 "card",
		"Zoe":              "zoe",
		"  spaced  ":       "spaced",
		"少女":               "少女",
	}
	for in, want := range cases {
		got := Slug(in)
		if got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
		if strings.ContainsAny(got, `/\`) {
			t.Errorf("Slug(%q) = %q contains a path separator", in, got)
		}
		if got == "." || got == ".." {
			t.Errorf("Slug(%q) = %q is a traversal segment", in, got)
		}
	}
}

// TestDecomposeWritesThePlannedLayout pins the file layout from the project
// plan: original, meta, character, greetings, lorebook, assets, dist.
func TestDecomposeWritesThePlannedLayout(t *testing.T) {
	original := buildFixture(t, KeywordChara, sampleCard())
	png, c, err := ParsePNGAndCard(original)
	if err != nil {
		t.Fatalf("ParsePNGAndCard: %v", err)
	}
	l, err := Decompose(t.TempDir(), png, c, "TestChar.png")
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	for _, path := range []string{
		l.OriginalPath(), l.MetaPath(), l.CharacterPath(),
		filepath.Join(l.GreetingsDir(), "default.md"),
		filepath.Join(l.GreetingsDir(), "alternate_1.md"),
		filepath.Join(l.GreetingsDir(), "alternate_2.md"),
		l.LorebookPath(), l.AssetsDir(),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s: %v", path, err)
		}
	}
	meta, err := l.LoadMeta()
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if meta.Name != "TestChar" || meta.Slug != "testchar" || meta.Keyword != KeywordChara {
		t.Fatalf("meta = %+v", meta)
	}
	greeting, err := os.ReadFile(filepath.Join(l.GreetingsDir(), "default.md"))
	if err != nil {
		t.Fatalf("read greeting: %v", err)
	}
	if string(greeting) != "Hello {{user}}!" {
		t.Fatalf("greeting = %q", greeting)
	}
}

// TestDecomposeRefusesDuplicateSlugCollision is a guard for a future
// multi-card workspace: the same slug must not silently merge two cards.
func TestDecomposeRefusesDuplicateSlugCollision(t *testing.T) {
	original := buildFixture(t, KeywordChara, sampleCard())
	png, c, err := ParsePNGAndCard(original)
	if err != nil {
		t.Fatalf("ParsePNGAndCard: %v", err)
	}
	root := t.TempDir()
	first, err := Decompose(root, png, c, "TestChar.png")
	if err != nil {
		t.Fatalf("first Decompose: %v", err)
	}
	// Re-decomposing the same card overwrites its own directory, which is the
	// intended idempotent behavior, and must leave exactly one directory.
	second, err := Decompose(root, png, c, "TestChar.png")
	if err != nil {
		t.Fatalf("second Decompose: %v", err)
	}
	if first.Root != second.Root {
		t.Fatalf("re-decomposing moved the directory: %q vs %q", first.Root, second.Root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("workspace has %d entries, want 1", len(entries))
	}
}

func base64Encode(b []byte) string {
	const table = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var out strings.Builder
	for i := 0; i < len(b); i += 3 {
		var n uint32
		rem := len(b) - i
		n = uint32(b[i]) << 16
		if rem > 1 {
			n |= uint32(b[i+1]) << 8
		}
		if rem > 2 {
			n |= uint32(b[i+2])
		}
		out.WriteByte(table[(n>>18)&0x3f])
		out.WriteByte(table[(n>>12)&0x3f])
		if rem > 1 {
			out.WriteByte(table[(n>>6)&0x3f])
		} else {
			out.WriteByte('=')
		}
		if rem > 2 {
			out.WriteByte(table[n&0x3f])
		} else {
			out.WriteByte('=')
		}
	}
	return out.String()
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// flatten renders a JSON document as leaf paths, so two documents can be
// compared without depending on number formatting.
func flatten(v any) map[string]string {
	out := map[string]string{}
	var walk func(prefix string, node any)
	walk = func(prefix string, node any) {
		switch t := node.(type) {
		case map[string]any:
			for k, child := range t {
				walk(prefix+"."+k, child)
			}
		case []any:
			for i, child := range t {
				walk(prefix+"["+itoa(i)+"]", child)
			}
		default:
			out[prefix] = normalize(node)
		}
	}
	walk("", v)
	return out
}

func normalize(v any) string {
	switch t := v.(type) {
	case nil:
		return "<nil>"
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// TestLooseFallbackReadsKeywordlessCard pins RP-Hub's tolerant reader: a card
// whose payload sits in an unknown text chunk still imports, as long as the
// value is long enough and looks like base64 JSON.
//
// Rejecting such a card would break real files that RP-Hub accepts.
func TestLooseFallbackReadsKeywordlessCard(t *testing.T) {
	doc := sampleCard()
	raw, _ := json.Marshal(doc)
	payload := base64Encode(raw)
	if len(payload) <= 50 {
		t.Fatalf("fixture payload too short to exercise the fallback: %d", len(payload))
	}
	png := &PNG{Chunks: []Chunk{
		{Type: "IHDR", Data: []byte{0, 0, 0, 1, 0, 0, 0, 1, 8, 2, 0, 0, 0}},
		makeTextChunk("some_other_key", payload),
		{Type: "IEND", Data: nil},
	}}
	_, c, err := ParsePNGAndCard(png.Bytes())
	if err != nil {
		t.Fatalf("a keywordless card was rejected: %v", err)
	}
	if c.Keyword != "" {
		t.Fatalf("keyword = %q, want empty for a fallback read", c.Keyword)
	}
	if name := c.Doc["data"].(map[string]any)["name"]; name != "TestChar" {
		t.Fatalf("name = %v", name)
	}
}

// TestShortUnknownChunkIsNotTreatedAsCard pins the fallback's bound: a short
// unrelated text chunk must not be mistaken for a card payload.
func TestShortUnknownChunkIsNotTreatedAsCard(t *testing.T) {
	png := &PNG{Chunks: []Chunk{
		{Type: "IHDR", Data: []byte{0, 0, 0, 1, 0, 0, 0, 1, 8, 2, 0, 0, 0}},
		makeTextChunk("Comment", "{short}"),
		{Type: "IEND", Data: nil},
	}}
	if _, _, err := ParsePNGAndCard(png.Bytes()); err == nil {
		t.Fatal("a short unknown chunk was accepted as a card")
	}
}

// TestChunkInsertedAfterIHDR pins the write position. RP-Hub inserts the card
// chunk immediately after IHDR, and a reader that assumes a position (or a
// byte-comparison test) depends on that being stable.
func TestChunkInsertedAfterIHDR(t *testing.T) {
	png := &PNG{Chunks: []Chunk{
		{Type: "IHDR", Data: []byte{0, 0, 0, 1, 0, 0, 0, 1, 8, 2, 0, 0, 0}},
		{Type: "IDAT", Data: []byte{1, 2, 3}},
		{Type: "IEND", Data: nil},
	}}
	if err := png.SetCard(KeywordChara, sampleCard()); err != nil {
		t.Fatalf("SetCard: %v", err)
	}
	if png.Chunks[0].Type != "IHDR" {
		t.Fatalf("first chunk = %s, want IHDR", png.Chunks[0].Type)
	}
	if png.Chunks[1].Type != "tEXt" {
		t.Fatalf("second chunk = %s, want the card tEXt right after IHDR", png.Chunks[1].Type)
	}
	if png.Chunks[2].Type != "IDAT" {
		t.Fatalf("third chunk = %s, want the original IDAT", png.Chunks[2].Type)
	}
}

// TestSetCardDoesNotDropOtherKeywordChunks pins that writing one keyword leaves
// the other payload in place: RP-Hub does not remove it, and dropping it would
// change which card a different reader sees.
func TestSetCardDoesNotDropOtherKeywordChunks(t *testing.T) {
	png := &PNG{Chunks: []Chunk{
		{Type: "IHDR", Data: []byte{0, 0, 0, 1, 0, 0, 0, 1, 8, 2, 0, 0, 0}},
		makeTextChunk(KeywordCCV3, base64Encode([]byte(`{"data":{"name":"Kept"}}`))),
		{Type: "IEND", Data: nil},
	}}
	if err := png.SetCard(KeywordChara, sampleCard()); err != nil {
		t.Fatalf("SetCard: %v", err)
	}
	_, c, err := ParsePNGAndCard(png.Bytes())
	if err != nil {
		t.Fatalf("ParsePNGAndCard: %v", err)
	}
	// chara wins, and ccv3 is still present for a reader that prefers it.
	if c.Keyword != KeywordChara {
		t.Fatalf("keyword = %q, want chara", c.Keyword)
	}
	found := false
	for _, ch := range png.Chunks {
		if k, _, ok := textPayload(ch); ok && k == KeywordCCV3 {
			found = true
		}
	}
	if !found {
		t.Fatal("the ccv3 chunk was dropped")
	}
}

// TestNormalizeReadsHistoricalSpellings pins the field aliases RP-Hub accepts:
// V1 cards spell the name and description differently, and a card that imports
// in RP-Hub must import here.
func TestNormalizeReadsHistoricalSpellings(t *testing.T) {
	v1 := map[string]any{
		"char_name":      "Legacy",
		"char_persona":   "An old-style card",
		"first_mes":      "Hi",
		"creatorcomment": "made long ago",
	}
	d := Normalize(v1)
	if d.Name != "Legacy" {
		t.Fatalf("Name = %q, want Legacy", d.Name)
	}
	if d.Description != "An old-style card" {
		t.Fatalf("Description = %q", d.Description)
	}
	if d.CreatorNotes != "made long ago" {
		t.Fatalf("CreatorNotes = %q", d.CreatorNotes)
	}
}

// TestNormalizeReadsWorldInfoFromBothContainers pins that `entries` may be a
// list or an object, and that the book may sit on the inner object or the
// document.
func TestNormalizeReadsWorldInfoFromBothContainers(t *testing.T) {
	asList := map[string]any{"data": map[string]any{
		"name": "A",
		"character_book": map[string]any{"entries": []any{
			map[string]any{"keys": []any{"k"}, "content": "c"},
		}},
	}}
	if d := Normalize(asList); len(d.WorldInfo) != 1 {
		t.Fatalf("list container: got %d entries, want 1", len(d.WorldInfo))
	}

	asMap := map[string]any{"data": map[string]any{
		"name": "B",
		"character_book": map[string]any{"entries": map[string]any{
			"0": map[string]any{"keys": []any{"k"}, "content": "c"},
		}},
	}}
	if d := Normalize(asMap); len(d.WorldInfo) != 1 {
		t.Fatalf("map container: got %d entries, want 1", len(d.WorldInfo))
	}

	onDocument := map[string]any{
		"name":           "C",
		"character_book": []any{map[string]any{"keys": []any{"k"}, "content": "c"}},
	}
	if d := Normalize(onDocument); len(d.WorldInfo) != 1 {
		t.Fatalf("document-level book: got %d entries, want 1", len(d.WorldInfo))
	}
}

// TestExportShapeMatchesRPHub pins the export document: `{data:{...}}` with the
// watermark, no top-level spec fields, and `character_book` only when entries
// exist.
func TestExportShapeMatchesRPHub(t *testing.T) {
	d := Document{
		Name:        "Exported",
		Description: "desc",
		Personality: "pers",
		FirstMes:    "hi",
	}
	doc := BuildExport(d, ExportOptions{})

	if _, ok := doc["spec"]; ok {
		t.Fatal("export wrote a top-level spec field, which RP-Hub does not")
	}
	data, ok := doc["data"].(map[string]any)
	if !ok {
		t.Fatalf("export has no data object: %#v", doc)
	}
	if data["creator_notes"] != ExportedCreatorNotes {
		t.Fatalf("creator_notes = %v, want the RP-Hub default", data["creator_notes"])
	}
	ext, ok := data["extensions"].(map[string]any)
	if !ok {
		t.Fatalf("export has no extensions: %#v", data)
	}
	if ext["rp_hub_watermark"] != Watermark {
		t.Fatalf("watermark = %v, want %q", ext["rp_hub_watermark"], Watermark)
	}
	if _, ok := data["character_book"]; ok {
		t.Fatal("character_book was written with no entries")
	}
	// UI templates are included by default, in both places RP-Hub puts them.
	if _, ok := data["uiTemplates"]; !ok {
		t.Fatal("uiTemplates missing from the default export")
	}
	if _, ok := ext["rp_hub_ui_templates"]; !ok {
		t.Fatal("rp_hub_ui_templates missing from the default export")
	}
}

// TestExportOptionsOmitPieces pins the opt-outs.
func TestExportOptionsOmitPieces(t *testing.T) {
	doc := BuildExport(Document{Name: "X"}, ExportOptions{OmitUITemplates: true, OmitWatermark: true})
	data := doc["data"].(map[string]any)
	if _, ok := data["uiTemplates"]; ok {
		t.Fatal("OmitUITemplates did not remove uiTemplates")
	}
	ext := data["extensions"].(map[string]any)
	if _, ok := ext["rp_hub_watermark"]; ok {
		t.Fatal("OmitWatermark did not remove the watermark")
	}
}

// TestExportWithWorldInfoWritesCharacterBook pins that entries produce a
// character_book, and that the entry defaults match RP-Hub's.
func TestExportWithWorldInfoWritesCharacterBook(t *testing.T) {
	d := Document{
		Name: "W",
		WorldInfo: []any{
			map[string]any{"keys": []any{"lab"}, "content": "A lab."},
		},
	}
	doc := BuildExport(d, ExportOptions{})
	data := doc["data"].(map[string]any)
	book, ok := data["character_book"].(map[string]any)
	if !ok {
		t.Fatalf("no character_book in %#v", data)
	}
	entries, ok := book["entries"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("entries = %#v", book["entries"])
	}
	entry := NormalizeWorldInfoEntry(entries[0])
	// RP-Hub's defaults.
	if !entry.Enabled {
		t.Fatal("enabled default should be true")
	}
	if entry.Scope != "character" {
		t.Fatalf("scope = %q, want character", entry.Scope)
	}
	if entry.Position != "at_depth" {
		t.Fatalf("position = %q, want at_depth", entry.Position)
	}
	if entry.Depth != 4 {
		t.Fatalf("depth = %v, want 4", entry.Depth)
	}
	if entry.Probability != 100 {
		t.Fatalf("probability = %v, want 100", entry.Probability)
	}
	if !entry.UseProbability {
		t.Fatal("useProbability default should be true")
	}
}

// TestExportedCardIsReadableAndWatermarked pins the full loop: export, pack,
// read back, and recognize the card as RP-Hub's.
func TestExportedCardIsReadableAndWatermarked(t *testing.T) {
	doc := BuildExport(Document{Name: "RoundTrip", FirstMes: "hi"}, ExportOptions{})
	png := &PNG{Chunks: []Chunk{
		{Type: "IHDR", Data: []byte{0, 0, 0, 1, 0, 0, 0, 1, 8, 2, 0, 0, 0}},
		{Type: "IEND", Data: nil},
	}}
	if err := png.SetCard(KeywordChara, doc); err != nil {
		t.Fatalf("SetCard: %v", err)
	}
	_, c, err := ParsePNGAndCard(png.Bytes())
	if err != nil {
		t.Fatalf("ParsePNGAndCard: %v", err)
	}
	if !c.IsFromRPHub() {
		t.Fatal("an exported card was not recognized as RP-Hub's")
	}
	if got := Normalize(c.Doc).Name; got != "RoundTrip" {
		t.Fatalf("name = %q", got)
	}
}
