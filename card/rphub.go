package card

import (
	"encoding/json"
	"strings"
)

// Document is RP-Hub's normalized view of a card.
//
// The card JSON in the wild comes in three shapes: V2 (`{spec, spec_version,
// data:{...}}`), V3 (same envelope, different spec string), and V1 (fields at
// the top level). RP-Hub reads all three by preferring `data` when it is an
// object and falling back to the document itself, and by accepting several
// historical spellings per field (`char_name` for `name`, `char_persona` for
// `description`, `creatorcomment` for `creator_notes`).
//
// The normalization below reproduces `parseImportedCharacterCard` from
// STA1N156/RP-Hub assets/js/core-utils.js.
type Document struct {
	Name         string
	Description  string
	Personality  string
	FirstMes     string
	CreatorNotes string
	// RegexScripts are the character's regex transformations.
	RegexScripts []any
	// UITemplates are RP-Hub's UI templates.
	UITemplates []any
	// WorldInfo are the character_book entries, already flattened from whatever
	// container the card used.
	WorldInfo []any
}

// inner returns the object the card's fields live in: `data` when present, else
// the document itself (the V1 layout).
func inner(doc map[string]any) map[string]any {
	if d, ok := doc["data"].(map[string]any); ok {
		return d
	}
	return doc
}

// firstString returns the first non-empty string among the candidate keys,
// checked in the inner object and then the document, which is how RP-Hub
// resolves the alternate spellings.
func firstString(inner, doc map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := inner[k].(string); ok && v != "" {
			return v
		}
	}
	for _, k := range keys {
		if v, ok := doc[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// firstSlice returns the first array-valued candidate.
func firstSlice(inner, doc map[string]any, keys ...string) []any {
	for _, k := range keys {
		if v, ok := inner[k].([]any); ok {
			return v
		}
	}
	for _, k := range keys {
		if v, ok := doc[k].([]any); ok {
			return v
		}
	}
	return nil
}

// asSlice converts a value that may be an array or an object into an array.
//
// World info arrives both ways: some cards write `character_book.entries` as a
// list, others as an object keyed by entry id. RP-Hub accepts both.
func asSlice(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	case map[string]any:
		out := make([]any, 0, len(t))
		for _, item := range t {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

// Normalize reads a card document into RP-Hub's field set.
func Normalize(doc map[string]any) Document {
	innerDoc := inner(doc)
	out := Document{
		Name: firstString(innerDoc, doc, "name", "char_name"),
		Description: firstString(innerDoc, doc,
			"description", "char_persona"),
		Personality:  firstString(innerDoc, doc, "personality"),
		FirstMes:     firstString(innerDoc, doc, "first_mes"),
		CreatorNotes: firstString(innerDoc, doc, "creator_notes", "creatorcomment", "creator_comment"),
	}
	if out.Name == "" {
		out.Name = "Unknown"
	}

	out.RegexScripts = firstSlice(innerDoc, doc, "regex_scripts")
	if len(out.RegexScripts) == 0 {
		if ext, ok := innerDoc["extensions"].(map[string]any); ok {
			out.RegexScripts = asSlice(ext["regex_scripts"])
		}
		if len(out.RegexScripts) == 0 {
			if ext, ok := doc["extensions"].(map[string]any); ok {
				out.RegexScripts = asSlice(ext["regex_scripts"])
			}
		}
	}

	out.UITemplates = firstSlice(innerDoc, doc, "uiTemplates", "ui_templates")
	if len(out.UITemplates) == 0 {
		for _, container := range []map[string]any{innerDoc, doc} {
			ext, ok := container["extensions"].(map[string]any)
			if !ok {
				continue
			}
			if v := asSlice(ext["ui_templates"]); len(v) > 0 {
				out.UITemplates = v
				break
			}
			if v := asSlice(ext["rp_hub_ui_templates"]); len(v) > 0 {
				out.UITemplates = v
				break
			}
		}
	}

	// World info: `character_book` on the inner object, then on the document,
	// and its `entries` may be a list or a map.
	for _, container := range []map[string]any{innerDoc, doc} {
		book, ok := container["character_book"]
		if !ok {
			continue
		}
		switch t := book.(type) {
		case map[string]any:
			if v := asSlice(t["entries"]); len(v) > 0 {
				out.WorldInfo = v
			}
		case []any:
			out.WorldInfo = t
		}
		if len(out.WorldInfo) > 0 {
			break
		}
	}
	return out
}

// ExportOptions controls how a document is built for export.
type ExportOptions struct {
	// CreatorNotes overrides the creator note. Empty means the RP-Hub default.
	CreatorNotes string
	// OmitUITemplates drops the UI templates from the export, matching the
	// `includeUiTemplates: false` option RP-Hub supports.
	OmitUITemplates bool
	// OmitWatermark drops the `rp_hub_watermark` marker. It exists so a caller
	// can produce a card that does not advertise this toolchain.
	OmitWatermark bool
	// WorldInfoMapper and RegexScriptMapper rewrite entries before export.
	// Nil keeps the entries as they are.
	WorldInfoMapper   func(any) any
	RegexScriptMapper func(any) any
	UITemplateMapper  func(any) any
}

// BuildExport produces RP-Hub's export document shape.
//
// The shape is deliberately minimal and matches `buildCharacterCardData`: a
// single `data` object with the core fields, the regex scripts and UI templates
// under `extensions`, and `character_book` present only when there are entries.
// No top-level `spec`/`spec_version` is written, because RP-Hub does not write
// them and a reader that keys off them would misclassify the card.
func BuildExport(d Document, opts ExportOptions) map[string]any {
	notes := opts.CreatorNotes
	if notes == "" {
		notes = ExportedCreatorNotes
	}

	data := map[string]any{
		"name":          d.Name,
		"description":   d.Description,
		"personality":   d.Personality,
		"first_mes":     d.FirstMes,
		"creator_notes": notes,
	}

	regexScripts := mapEach(d.RegexScripts, opts.RegexScriptMapper)
	uiTemplates := mapEach(d.UITemplates, opts.UITemplateMapper)

	extensions := map[string]any{
		"regex_scripts": regexScripts,
	}
	if !opts.OmitWatermark {
		extensions["rp_hub_watermark"] = Watermark
	}
	if !opts.OmitUITemplates {
		data["uiTemplates"] = uiTemplates
		extensions["rp_hub_ui_templates"] = uiTemplates
	}
	data["extensions"] = extensions

	entries := mapEach(d.WorldInfo, opts.WorldInfoMapper)
	if len(entries) > 0 {
		data["character_book"] = map[string]any{"entries": entries}
	}

	return map[string]any{"data": data}
}

func mapEach(items []any, mapper func(any) any) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		if mapper != nil {
			item = mapper(item)
		}
		out = append(out, item)
	}
	return out
}

// WorldInfoEntry is RP-Hub's normalized world-info entry.
type WorldInfoEntry struct {
	Comment        string
	Content        string
	Enabled        bool
	Scope          string
	Keys           []string
	UseRegex       bool
	Constant       bool
	Position       string
	Order          float64
	Depth          float64
	ScanDepth      float64
	HasScanDepth   bool
	Probability    float64
	UseProbability bool
}

// NormalizeWorldInfoEntry reads an entry with RP-Hub's defaults, which are what
// make an entry behave identically after a round trip.
func NormalizeWorldInfoEntry(v any) WorldInfoEntry {
	m, _ := v.(map[string]any)
	e := WorldInfoEntry{
		Enabled:        boolOr(m["enabled"], true),
		Scope:          stringOr(m["scope"], "character"),
		UseRegex:       boolOr(m["useRegex"], false),
		Constant:       boolOr(m["constant"], false),
		Position:       stringOr(m["position"], "at_depth"),
		Order:          numberOr(m["order"], 0),
		Depth:          numberOr(m["depth"], 4),
		Probability:    numberOr(m["probability"], 100),
		UseProbability: boolOr(m["useProbability"], true),
	}
	e.Comment = firstString(m, nil, "comment", "name")
	e.Content = firstString(m, nil, "content")
	if keys, ok := m["keys"].([]any); ok {
		for _, k := range keys {
			if s, ok := k.(string); ok {
				e.Keys = append(e.Keys, s)
			}
		}
	}
	if _, ok := m["scanDepth"]; ok && m["scanDepth"] != nil {
		e.ScanDepth = numberOr(m["scanDepth"], 0)
		e.HasScanDepth = true
	}
	return e
}

// ToMap renders an entry back to RP-Hub's export shape.
func (e WorldInfoEntry) ToMap() map[string]any {
	out := map[string]any{
		"comment":        e.Comment,
		"content":        e.Content,
		"enabled":        e.Enabled,
		"scope":          e.Scope,
		"keys":           stringsToAny(e.Keys),
		"useRegex":       e.UseRegex,
		"constant":       e.Constant,
		"position":       e.Position,
		"order":          e.Order,
		"depth":          e.Depth,
		"probability":    e.Probability,
		"useProbability": e.UseProbability,
	}
	if e.HasScanDepth {
		out["scanDepth"] = e.ScanDepth
	} else {
		out["scanDepth"] = nil
	}
	return out
}

func stringsToAny(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

func boolOr(v any, def bool) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

func stringOr(v any, def string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}

func numberOr(v any, def float64) float64 {
	switch t := v.(type) {
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return f
		}
	case float64:
		return t
	case int:
		return float64(t)
	}
	return def
}

// watermarkOf reports the card's watermark marker, when it carries one.
func watermarkOf(doc map[string]any) string {
	for _, container := range []map[string]any{inner(doc), doc} {
		ext, ok := container["extensions"].(map[string]any)
		if !ok {
			continue
		}
		if w, ok := ext["rp_hub_watermark"].(string); ok {
			return w
		}
	}
	return ""
}

// IsFromRPHub reports whether a card advertises the RP-Hub watermark.
func (c *Card) IsFromRPHub() bool { return watermarkOf(c.Doc) == Watermark }

// TrimmedDescription is a convenience for callers that display a preview.
func (d Document) TrimmedDescription(limit int) string {
	if limit <= 0 || len(d.Description) <= limit {
		return d.Description
	}
	return strings.TrimSpace(d.Description[:limit]) + "..."
}
