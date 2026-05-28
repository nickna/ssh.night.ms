package auth

import "strings"

// DefaultDeniedUsernames is the built-in list of common system/service
// account names used in brute-force SSH scans. Matching is case-insensitive
// and applied at the Lookup layer before any rate-limiter / DB / Argon2id
// work runs, so noise traffic costs O(1) instead of a Redis round-trip plus
// a Postgres query plus an Argon2id evaluation.
var DefaultDeniedUsernames = []string{
	"root", "admin", "administrator",
	"oracle", "postgres", "mysql", "mssql", "redis", "mongodb",
	"elasticsearch", "kibana",
	"nginx", "apache", "httpd", "www-data", "tomcat",
	"ftp", "ftpuser", "anonymous", "mail", "postfix", "sshd",
	"ubuntu", "debian", "centos", "fedora", "alpine", "pi", "ec2-user",
	"jenkins", "gitlab", "git", "hadoop", "vagrant",
	"operator", "support", "service", "default",
	"test", "guest", "user",
}

// UsernameDenylist is a set of handles refused at the auth layer before any
// expensive work. A nil receiver is the disabled state and Denied always
// returns false, which lets callers wire the denylist unconditionally.
type UsernameDenylist struct {
	set map[string]struct{}
}

// NewUsernameDenylist builds a denylist from the given handles. Empty,
// whitespace-only, or duplicate entries are normalized away. Returns nil
// when the resulting set is empty so callers can compare against nil to
// detect the disabled state.
func NewUsernameDenylist(handles []string) *UsernameDenylist {
	if len(handles) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(handles))
	for _, h := range handles {
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" {
			set[h] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nil
	}
	return &UsernameDenylist{set: set}
}

// Denied reports whether handle is on the denylist. Case-insensitive. A nil
// receiver returns false.
func (d *UsernameDenylist) Denied(handle string) bool {
	if d == nil {
		return false
	}
	_, ok := d.set[strings.ToLower(handle)]
	return ok
}
