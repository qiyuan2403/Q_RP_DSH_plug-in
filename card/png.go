// Package card reads and writes the character cards RP-Hub exchanges.
//
// COMPATIBILITY SOURCE. The behavior here is aligned to RP-Hub's own
// implementation (STA1N156/RP-Hub, assets/js/core-utils.js:
// `readPngChunks`, `findPngCharacterPayload`, `parseCharacterPayload`,
// `injectPngTextChunk`, `buildCharacterCardData`, `parseImportedCharacterCard`),
// because RP-Hub is the ecosystem this harness produces cards for. Where a
// general-purpose reader would differ, RP-Hub wins.
//
// The four points that are easy to get wrong and are pinned by tests:
//
//  1. READ PRIORITY is `chara` FIRST, then `ccv3`. A card carrying both is read
//     as its `chara` payload.
//  2. A card with neither keyword still reads, via the loose fallback: any text
//     chunk whose value is longer than 50 characters and starts with `{` or
//     `ey` (a base64-encoded JSON object).
//  3. WRITE POSITION is immediately after IHDR, which is where RP-Hub inserts.
//  4. The exported document is `{data: {...}}` with an
//     `extensions.rp_hub_watermark` marker, and no top-level spec fields.
//
// Nothing here needs JavaScript: card handling is the product's core, so it
// must work on a host with no Node installed at all.
package card

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"strings"
)

// Chunk keywords, in RP-Hub's read priority order.
const (
	// KeywordChara is read first.
	KeywordChara = "chara"
	// KeywordCCV3 is read when `chara` is absent.
	KeywordCCV3 = "ccv3"
)

// Watermark is the marker RP-Hub writes into exported cards.
const Watermark = "rp-hub"

// ExportedCreatorNotes is the default creator note on an RP-Hub export.
const ExportedCreatorNotes = "Exported from RolePlay Hub"

// fallbackMinLength is RP-Hub's threshold for the keyword-less fallback.
const fallbackMinLength = 50

var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// ErrNotPNG reports a file that is not a PNG.
var ErrNotPNG = errors.New("card: not a PNG")

// ErrNoCard reports a PNG with no character-card payload.
var ErrNoCard = errors.New("card: PNG carries no character payload")

// Chunk is one PNG chunk, preserved verbatim so a round trip is lossless.
type Chunk struct {
	Type string
	Data []byte
}

// PNG is a parsed PNG with its chunks in order.
type PNG struct {
	Chunks []Chunk
}

// ParsePNG splits a PNG into its chunks.
//
// CRCs are validated. RP-Hub skips the check (it reads chunks defensively and
// ignores failures), but an import that silently accepts a mangled file writes
// the corruption back on export, so this host refuses instead.
func ParsePNG(data []byte) (*PNG, error) {
	if len(data) < len(pngSignature) || !bytes.Equal(data[:len(pngSignature)], pngSignature) {
		return nil, ErrNotPNG
	}
	out := &PNG{}
	pos := len(pngSignature)
	for pos < len(data) {
		if pos+8 > len(data) {
			return nil, fmt.Errorf("card: truncated chunk header at offset %d", pos)
		}
		length := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		ctype := string(data[pos+4 : pos+8])
		bodyStart := pos + 8
		bodyEnd := bodyStart + length
		if bodyEnd+4 > len(data) {
			return nil, fmt.Errorf("card: chunk %q overruns the file (need %d bytes at %d)", ctype, length, bodyStart)
		}
		wantCRC := binary.BigEndian.Uint32(data[bodyEnd : bodyEnd+4])
		gotCRC := crc32.ChecksumIEEE(data[pos+4 : bodyEnd])
		if wantCRC != gotCRC {
			return nil, fmt.Errorf("card: chunk %q CRC mismatch (got %08x, want %08x)", ctype, gotCRC, wantCRC)
		}
		out.Chunks = append(out.Chunks, Chunk{Type: ctype, Data: append([]byte(nil), data[bodyStart:bodyEnd]...)})
		pos = bodyEnd + 4
		if ctype == "IEND" {
			break
		}
	}
	if len(out.Chunks) == 0 || out.Chunks[len(out.Chunks)-1].Type != "IEND" {
		return nil, errors.New("card: PNG is missing IEND")
	}
	return out, nil
}

// Bytes re-encodes the PNG, recomputing every CRC.
func (p *PNG) Bytes() []byte {
	var buf bytes.Buffer
	buf.Write(pngSignature)
	for _, c := range p.Chunks {
		var hdr [8]byte
		binary.BigEndian.PutUint32(hdr[0:4], uint32(len(c.Data)))
		copy(hdr[4:8], c.Type)
		buf.Write(hdr[:])
		buf.Write(c.Data)
		crc := crc32.NewIEEE()
		crc.Write([]byte(c.Type))
		crc.Write(c.Data)
		var tail [4]byte
		binary.BigEndian.PutUint32(tail[:], crc.Sum32())
		buf.Write(tail[:])
	}
	return buf.Bytes()
}

// textPayload decodes a tEXt or iTXt chunk into (keyword, text).
//
// The iTXt layout is keyword NUL compressionFlag NUL method NUL language NUL
// translated NUL text; a compressed payload is refused rather than returned
// garbled, because no card in this ecosystem writes one.
func textPayload(c Chunk) (keyword, text string, ok bool) {
	switch c.Type {
	case "tEXt":
		idx := bytes.IndexByte(c.Data, 0)
		if idx < 0 {
			return "", "", false
		}
		return string(c.Data[:idx]), string(c.Data[idx+1:]), true
	case "iTXt":
		parts := bytes.SplitN(c.Data, []byte{0}, 6)
		if len(parts) < 6 {
			return "", "", false
		}
		if len(parts[1]) > 0 && parts[1][0] != 0 {
			return "", "", false
		}
		return string(parts[0]), string(parts[5]), true
	default:
		return "", "", false
	}
}

// makeTextChunk builds a tEXt chunk carrying keyword -> value.
func makeTextChunk(keyword, value string) Chunk {
	data := append([]byte(keyword), 0)
	data = append(data, []byte(value)...)
	return Chunk{Type: "tEXt", Data: data}
}

// Card is a parsed character card: the raw JSON plus the decoded document.
type Card struct {
	// Keyword is the chunk keyword the payload came from, or "" when it came
	// from the loose fallback.
	Keyword string
	// Raw is the decoded JSON bytes, preserved for lossless write-back.
	Raw []byte
	// Doc is the decoded document, with numbers preserved as json.Number.
	Doc map[string]any
}

// ParsePNGAndCard extracts the card from a PNG.
func ParsePNGAndCard(data []byte) (*PNG, *Card, error) {
	png, err := ParsePNG(data)
	if err != nil {
		return nil, nil, err
	}
	card, err := png.Card()
	if err != nil {
		return nil, nil, err
	}
	return png, card, nil
}

// Card extracts the card document, following RP-Hub's lookup order.
func (p *PNG) Card() (*Card, error) {
	texts := map[string]string{}
	var order []string
	for _, c := range p.Chunks {
		k, v, ok := textPayload(c)
		if !ok {
			continue
		}
		if _, seen := texts[k]; !seen {
			texts[k] = v
			order = append(order, k)
		}
	}

	// 1. `chara` first, then `ccv3` — RP-Hub's priority.
	payload, keyword := "", ""
	if v, ok := texts[KeywordChara]; ok && strings.TrimSpace(v) != "" {
		payload, keyword = v, KeywordChara
	} else if v, ok := texts[KeywordCCV3]; ok && strings.TrimSpace(v) != "" {
		payload, keyword = v, KeywordCCV3
	}

	// 2. Loose fallback: any long text chunk that looks like a base64 JSON
	// object. This is how RP-Hub reads cards whose keyword it does not know.
	if payload == "" {
		for _, k := range order {
			v := strings.TrimSpace(texts[k])
			if len(v) > fallbackMinLength && (strings.HasPrefix(v, "{") || strings.HasPrefix(v, "ey")) {
				payload, keyword = v, ""
				break
			}
		}
	}
	if payload == "" {
		return nil, ErrNoCard
	}

	// 3. Decode: base64 first, then a raw JSON string, as RP-Hub does.
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
	if err != nil {
		raw = []byte(strings.TrimSpace(payload))
	}
	doc := map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("card: payload is not valid JSON: %w", err)
	}
	return &Card{Keyword: keyword, Raw: raw, Doc: doc}, nil
}

// SetCard writes a card payload, inserting after IHDR exactly where RP-Hub does.
//
// An existing chunk with the same keyword is replaced IN PLACE so its position
// is stable; otherwise the new chunk is inserted right after IHDR, which is
// RP-Hub's insertion point. Other keyword chunks are left alone: RP-Hub does
// not remove them, and dropping a payload a reader might prefer would silently
// change which card a third-party tool sees.
func (p *PNG) SetCard(keyword string, doc any) error {
	raw, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("card: encode: %w", err)
	}
	fresh := makeTextChunk(keyword, base64.StdEncoding.EncodeToString(raw))

	for i, c := range p.Chunks {
		if k, _, ok := textPayload(c); ok && k == keyword {
			p.Chunks[i] = fresh
			return nil
		}
	}

	insert := p.insertionPoint()
	p.Chunks = append(p.Chunks[:insert], append([]Chunk{fresh}, p.Chunks[insert:]...)...)
	return nil
}

// insertionPoint is the index just after IHDR, falling back to just after the
// signature when IHDR is absent.
func (p *PNG) insertionPoint() int {
	for i, c := range p.Chunks {
		if c.Type == "IHDR" {
			return i + 1
		}
	}
	return 0
}

// ReadFile loads a card from disk.
func ReadFile(path string) (*PNG, *Card, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return ParsePNGAndCard(data)
}
