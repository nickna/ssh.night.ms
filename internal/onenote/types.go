package onenote

import "time"

// The domain types here are deliberately decoupled from Microsoft Graph's
// JSON wire shapes (those live unexported in graph.go). Screens and REST
// handlers depend only on these, so a Graph schema wobble never leaks past
// the mapping layer.

// Notebook is one OneNote notebook in the user's personal store.
type Notebook struct {
	ID         string
	Name       string
	IsDefault  bool
	Color      string // notebook accent color as a hex string ("#A6A6A6"); "" when unset
	CreatedAt  time.Time
	ModifiedAt time.Time
	WebURL     string // oneNoteWebUrl — opens the notebook in a browser
}

// Section is a section within a notebook. NotebookID is the parent notebook's
// Graph id (empty when Graph didn't expand the parent).
type Section struct {
	ID         string
	Name       string
	NotebookID string
	CreatedAt  time.Time
	ModifiedAt time.Time
}

// Page is the metadata for a OneNote page (no content body). SectionID is the
// parent section's Graph id.
type Page struct {
	ID         string
	Title      string
	SectionID  string
	CreatedAt  time.Time
	ModifiedAt time.Time
	WebURL     string // oneNoteWebUrl
	ClientURL  string // oneNoteClientUrl (desktop app deep link)
}

// BlockKind discriminates the render-ready Block sum type. Image and Table are
// "non-text" kinds: they render as placeholders and cannot be faithfully
// round-tripped through the markdown edit buffer.
type BlockKind int

const (
	BlockParagraph BlockKind = iota
	BlockHeading
	BlockQuote
	BlockCode
	BlockList
	BlockImage // placeholder "[image: alt]"; lost on a text rewrite
	BlockTable // placeholder pre-formatted text; not editable in v1
)

// nonText reports whether a block kind can't survive the text round-trip.
func (k BlockKind) nonText() bool { return k == BlockImage || k == BlockTable }

// String returns the stable lowercase token used in the REST API and logs.
func (k BlockKind) String() string {
	switch k {
	case BlockHeading:
		return "heading"
	case BlockQuote:
		return "quote"
	case BlockCode:
		return "code"
	case BlockList:
		return "list"
	case BlockImage:
		return "image"
	case BlockTable:
		return "table"
	default:
		return "paragraph"
	}
}

// Block is one render-ready unit of page body, modeled on internal/reader's
// Block vocabulary. Text holds the textual payload (image alt for BlockImage);
// URL is the image src for BlockImage and empty otherwise.
type Block struct {
	Kind BlockKind
	Text string
	URL  string
}

// EditableElement is a top-level page element that carried a generated id in
// the includeIDs=true content fetch. ID is exactly what a PATCH command
// targets as "#<ID>". Kind/Text mirror the corresponding Block so a caller can
// present a targeted single-element edit.
type EditableElement struct {
	ID   string
	Kind BlockKind
	Text string
}

// PageContent is a fetched page: its metadata, the render-ready blocks, the
// editable-element map (for targeted edits), and the raw includeIDs HTML
// (retained so an edit can reason about the live element ids). HasNonText is
// true when any block is an image/table/etc — the signal the ReplaceBody
// confirmation gate keys on.
type PageContent struct {
	Page
	Blocks     []Block
	Elements   []EditableElement
	HTML       string
	HasNonText bool
}

// NewPage is the input to CreatePage. Markdown is converted to the minimal
// OneNote HTML subset; Title becomes the page <title>.
type NewPage struct {
	Title    string
	Markdown string
}

// cachedPage is the jsonb shape persisted in onenote_page_cache.blocks. Kept
// separate from PageContent so the on-disk format is explicit and stable.
type cachedPage struct {
	Blocks     []Block           `json:"blocks"`
	Elements   []EditableElement `json:"elements"`
	HasNonText bool              `json:"has_non_text"`
}
