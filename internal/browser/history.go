package browser

// historyCap bounds the per-session back/forward stack. Entries beyond the
// cap are dropped from the oldest end. 100 covers any realistic browsing
// session inside a single SSH connection without unbounded growth.
const historyCap = 100

// Entry is one record in the back/forward stack.
type Entry struct {
	URL   string
	Title string
}

// History is a back/forward stack with a single cursor. Push truncates any
// forward entries (the standard "navigate after backing up" semantic), then
// appends. Bounded by historyCap from the oldest end.
type History struct {
	entries []Entry
	pos     int // index of current entry; -1 when empty
}

// New returns an empty History.
func New() *History { return &History{pos: -1} }

// Push appends e as the new current entry, dropping any forward stack first.
// Duplicate consecutive URLs collapse so reload doesn't bloat the stack.
func (h *History) Push(e Entry) {
	if h == nil {
		return
	}
	if h.pos >= 0 && h.pos < len(h.entries) && h.entries[h.pos].URL == e.URL {
		// Same URL as current — just refresh the title and don't grow.
		h.entries[h.pos].Title = e.Title
		return
	}
	// Drop forward entries.
	if h.pos+1 < len(h.entries) {
		h.entries = h.entries[:h.pos+1]
	}
	h.entries = append(h.entries, e)
	h.pos = len(h.entries) - 1
	// Trim from the oldest end if over cap.
	if len(h.entries) > historyCap {
		excess := len(h.entries) - historyCap
		h.entries = h.entries[excess:]
		h.pos -= excess
	}
}

// Back returns the previous entry and moves the cursor. ok=false when there
// is no entry to go back to.
func (h *History) Back() (Entry, bool) {
	if h == nil || h.pos <= 0 {
		return Entry{}, false
	}
	h.pos--
	return h.entries[h.pos], true
}

// Forward is the mirror of Back. ok=false when at the head.
func (h *History) Forward() (Entry, bool) {
	if h == nil || h.pos+1 >= len(h.entries) {
		return Entry{}, false
	}
	h.pos++
	return h.entries[h.pos], true
}

// Current returns the entry at the cursor.
func (h *History) Current() (Entry, bool) {
	if h == nil || h.pos < 0 || h.pos >= len(h.entries) {
		return Entry{}, false
	}
	return h.entries[h.pos], true
}

// Position returns 1-based cursor position and total count, for the URL bar
// breadcrumb. Returns (0,0) when empty.
func (h *History) Position() (cur, total int) {
	if h == nil || h.pos < 0 {
		return 0, 0
	}
	return h.pos + 1, len(h.entries)
}

// CanBack reports whether Back would succeed.
func (h *History) CanBack() bool { return h != nil && h.pos > 0 }

// CanForward reports whether Forward would succeed.
func (h *History) CanForward() bool { return h != nil && h.pos+1 < len(h.entries) }
