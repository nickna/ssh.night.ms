package news

// Source pairs a Provider with the metadata the TUI needs to render a source
// picker: a stable string ID for persistence and lookup, a short DisplayName
// for tab labels, a Host string for the "fetching from …" copy, and a Hotkey
// rune the screen binds for direct jumps. The Provider is typically already
// wrapped in a Cache by the caller — the registry doesn't impose caching.
type Source struct {
	ID          string
	DisplayName string
	Host        string
	Hotkey      rune
	Provider    Provider
}

// Registry holds the ordered list of news Sources the BBS surfaces. Order is
// the user-facing tab order. Lookup by ID is constant-time via the map.
//
// The registry deliberately doesn't implement Provider — callers must pick a
// specific source. A merged-feed view is out of scope for the initial
// multi-source pass; if that lands later, it can wrap Registry with its own
// aggregator type rather than overloading this one.
type Registry struct {
	sources []Source
	byID    map[string]Source
}

// NewRegistry builds a Registry from the given Sources in order. Duplicate
// IDs are not permitted — later entries silently overwrite earlier ones in
// the lookup map but stay distinct in the ordered slice, which would render
// inconsistently. Callers are expected to validate uniqueness at
// construction time (typically a hardcoded list in main.go).
func NewRegistry(sources ...Source) *Registry {
	r := &Registry{
		sources: append([]Source(nil), sources...),
		byID:    make(map[string]Source, len(sources)),
	}
	for _, s := range sources {
		r.byID[s.ID] = s
	}
	return r
}

// Sources returns the ordered slice of registered Sources. Callers must not
// mutate the returned slice (it shares backing storage with the registry).
func (r *Registry) Sources() []Source {
	if r == nil {
		return nil
	}
	return r.sources
}

// Get returns the Source with the given ID and true; or a zero Source and
// false when not registered. Used by the News screen to resolve a stored
// preferred-source ID, and to ignore stale IDs from removed sources.
func (r *Registry) Get(id string) (Source, bool) {
	if r == nil {
		return Source{}, false
	}
	s, ok := r.byID[id]
	return s, ok
}

// Len returns the number of registered sources. Convenience for screens that
// want to early-out when nothing is registered.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	return len(r.sources)
}
