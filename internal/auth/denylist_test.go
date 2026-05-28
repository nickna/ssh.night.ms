package auth

import "testing"

func TestUsernameDenylistMatchesCaseInsensitive(t *testing.T) {
	d := NewUsernameDenylist([]string{"root"})
	if d == nil {
		t.Fatal("expected non-nil denylist")
	}
	for _, in := range []string{"root", "ROOT", "RoOt"} {
		if !d.Denied(in) {
			t.Errorf("Denied(%q) = false, want true", in)
		}
	}
	if d.Denied("alice") {
		t.Error("Denied(alice) = true, want false")
	}
}

func TestUsernameDenylistEmptyAndNil(t *testing.T) {
	if NewUsernameDenylist(nil) != nil {
		t.Error("NewUsernameDenylist(nil) = non-nil, want nil")
	}
	if NewUsernameDenylist([]string{}) != nil {
		t.Error("NewUsernameDenylist([]) = non-nil, want nil")
	}
	if NewUsernameDenylist([]string{"", "  ", "\t"}) != nil {
		t.Error("NewUsernameDenylist of all-blank = non-nil, want nil")
	}
	var d *UsernameDenylist
	if d.Denied("root") {
		t.Error("(*UsernameDenylist)(nil).Denied(root) = true, want false")
	}
}

func TestUsernameDenylistTrimsAndDropsEmpty(t *testing.T) {
	d := NewUsernameDenylist([]string{" root ", "", "  ", "Admin"})
	if d == nil {
		t.Fatal("expected non-nil denylist")
	}
	for _, in := range []string{"root", "admin"} {
		if !d.Denied(in) {
			t.Errorf("Denied(%q) = false, want true", in)
		}
	}
	if got := len(d.set); got != 2 {
		t.Errorf("len(set) = %d, want 2", got)
	}
}

func TestDefaultDeniedUsernamesContainsCanonical(t *testing.T) {
	d := NewUsernameDenylist(DefaultDeniedUsernames)
	if d == nil {
		t.Fatal("expected non-nil denylist from defaults")
	}
	for _, h := range []string{"root", "postgres", "oracle", "ubuntu", "pi"} {
		if !d.Denied(h) {
			t.Errorf("defaults missing %q", h)
		}
	}
}
