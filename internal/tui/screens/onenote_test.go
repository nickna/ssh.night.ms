package screens

import (
	"errors"
	"testing"

	"github.com/nickna/ssh.night.ms/internal/auth/usertoken"
	"github.com/nickna/ssh.night.ms/internal/onenote"
)

// ids extracts the node ids of the current tree for compact assertions.
func ids(m *OneNote) []string {
	out := make([]string, len(m.tree))
	for i, n := range m.tree {
		out[i] = n.id
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestTree_InsertAndCollapse(t *testing.T) {
	m := &OneNote{tree: []onenoteNode{
		{kind: nodeNotebook, id: "nb1", depth: 0},
		{kind: nodeNotebook, id: "nb2", depth: 0},
	}}

	// Expand nb1 with two sections.
	m.insertChildren("nb1", []onenoteNode{
		{kind: nodeSection, id: "s1", depth: 1, parentID: "nb1"},
		{kind: nodeSection, id: "s2", depth: 1, parentID: "nb1"},
	})
	if got := ids(m); !eq(got, []string{"nb1", "s1", "s2", "nb2"}) {
		t.Fatalf("after section insert: %v", got)
	}
	if !m.tree[0].expanded {
		t.Error("nb1 should be marked expanded")
	}

	// Expand s1 with two pages — they must nest between s1 and s2.
	m.insertChildren("s1", []onenoteNode{
		{kind: nodePage, id: "p1", depth: 2, parentID: "s1"},
		{kind: nodePage, id: "p2", depth: 2, parentID: "s1"},
	})
	if got := ids(m); !eq(got, []string{"nb1", "s1", "p1", "p2", "s2", "nb2"}) {
		t.Fatalf("after page insert: %v", got)
	}

	// Collapsing nb1 (index 0) drops every deeper-depth descendant block.
	m.collapseAt(0)
	if got := ids(m); !eq(got, []string{"nb1", "nb2"}) {
		t.Fatalf("after collapse: %v", got)
	}
	if m.tree[0].expanded {
		t.Error("nb1 should be collapsed")
	}
}

func TestCurrentSectionID(t *testing.T) {
	m := &OneNote{tree: []onenoteNode{
		{kind: nodeNotebook, id: "nb1", depth: 0},
		{kind: nodeSection, id: "s1", depth: 1, parentID: "nb1"},
		{kind: nodePage, id: "p1", depth: 2, parentID: "s1"},
	}}

	m.cursor = 0 // notebook → no section
	if _, ok := m.currentSectionID(); ok {
		t.Error("notebook cursor should not yield a section")
	}
	m.cursor = 1 // section
	if id, ok := m.currentSectionID(); !ok || id != "s1" {
		t.Errorf("section cursor = %q,%v", id, ok)
	}
	m.cursor = 2 // page → parent section
	if id, ok := m.currentSectionID(); !ok || id != "s1" {
		t.Errorf("page cursor = %q,%v", id, ok)
	}
}

func TestClassifyErr(t *testing.T) {
	cases := []struct {
		err  error
		want ctaKind
	}{
		{usertoken.ErrNoLink, ctaNoLink},
		{usertoken.ErrMissingScope, ctaScope},
		{usertoken.ErrNeedsReauth, ctaReauth},
		{onenote.ErrConfirmRequired, ctaNone},
		{errors.New("boom"), ctaNone},
		{nil, ctaNone},
	}
	for _, c := range cases {
		if got := classifyErr(c.err); got != c.want {
			t.Errorf("classifyErr(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestHandleServiceErr_TypedFlipsToCTA(t *testing.T) {
	m := &OneNote{}
	if !m.handleServiceErr(usertoken.ErrNoLink) {
		t.Fatal("expected typed error consumed")
	}
	if m.mode != onModeLinkCTA || m.cta != ctaNoLink {
		t.Errorf("mode=%v cta=%v", m.mode, m.cta)
	}

	m2 := &OneNote{}
	if !m2.handleServiceErr(errors.New("transient")) {
		t.Fatal("expected transient error consumed")
	}
	if m2.mode == onModeLinkCTA {
		t.Error("transient error should not flip to CTA")
	}
	if m2.notice == "" || m2.noticeKind != "err" {
		t.Errorf("expected error notice, got %q/%q", m2.notice, m2.noticeKind)
	}
}
