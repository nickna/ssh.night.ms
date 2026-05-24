package chat

import (
	"testing"

	"github.com/nickna/ssh.night.ms/internal/tui/nav"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want Command
		ok   bool
	}{
		{"", Command{}, false},
		{"   ", Command{}, false},
		{"hello", Command{}, false},
		{"/", Command{}, false},
		{"/help", Command{Name: "help"}, true},
		{"  /help  ", Command{Name: "help"}, true},
		{"/HELP", Command{Name: "help"}, true},
		{"/join #lobby", Command{Name: "join", Args: "#lobby"}, true},
		{"/join   #foo   bar", Command{Name: "join", Args: "#foo   bar"}, true},
		{"/me waves", Command{Name: "me", Args: "waves"}, true},
	}
	for _, tc := range cases {
		got, ok := Parse(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("Parse(%q) = (%+v, %v), want (%+v, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestDispatch_BasicCommands(t *testing.T) {
	cases := []struct {
		name string
		cmd  Command
		want Action
	}{
		{"help", Command{Name: "help"}, Notice{Text: helpText()}},
		{"quit", Command{Name: "quit"}, Navigate{Target: nav.DestLogout}},
		{"logout alias", Command{Name: "logout"}, Navigate{Target: nav.DestLogout}},
		{"lobby", Command{Name: "lobby"}, Navigate{Target: nav.DestLobby}},
		{"join with #", Command{Name: "join", Args: "#random"}, Switch{Channel: "random", EnsureJoin: true}},
		{"join without #", Command{Name: "join", Args: "random"}, Switch{Channel: "random", EnsureJoin: true}},
		{"join uppercase", Command{Name: "join", Args: "#RanDom"}, Switch{Channel: "random", EnsureJoin: true}},
		{"join missing arg", Command{Name: "join"}, Notice{Text: "usage: /join #channel-name"}},
		{"switch", Command{Name: "switch", Args: "lobby"}, Switch{Channel: "lobby"}},
		{"leave", Command{Name: "leave"}, Leave{}},
		{"edit", Command{Name: "edit", Args: "the new body"}, EditLast{Body: "the new body"}},
		{"edit missing arg", Command{Name: "edit"}, Notice{Text: "usage: /edit <new message body>"}},
		{"dm with @", Command{Name: "dm", Args: "@alice"}, OpenDM{Handle: "alice"}},
		{"dm without @", Command{Name: "dm", Args: "alice"}, OpenDM{Handle: "alice"}},
		{"dm missing arg", Command{Name: "dm"}, Notice{Text: "usage: /dm @handle"}},
		{"msg alias", Command{Name: "msg", Args: "@bob"}, OpenDM{Handle: "bob"}},
		{"me waves", Command{Name: "me", Args: "waves"}, Send{Body: MeMarker + "waves"}},
		{"me missing arg", Command{Name: "me"}, Notice{Text: "usage: /me <action>"}},
		{"who", Command{Name: "who"}, Who{}},
		{"users alias", Command{Name: "users"}, Who{}},
		{"reply", Command{Name: "reply", Args: "thread body"}, Reply{Body: "thread body"}},
		{"r alias", Command{Name: "r", Args: "x"}, Reply{Body: "x"}},
		{"reply missing arg", Command{Name: "reply"}, Notice{Text: "usage: /reply <message body>"}},
		{"react thumbsup", Command{Name: "react", Args: "👍"}, React{Emoji: "👍"}},
		{"+ alias", Command{Name: "+", Args: "❤️"}, React{Emoji: "❤️"}},
		{"react missing arg", Command{Name: "react"}, Notice{Text: "usage: /react <emoji>"}},
		{"unreact thumbsup", Command{Name: "unreact", Args: "👍"}, Unreact{Emoji: "👍"}},
		{"- alias", Command{Name: "-", Args: "🎉"}, Unreact{Emoji: "🎉"}},
		{"unknown", Command{Name: "unknown"}, Unknown{Name: "unknown"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Dispatch(tc.cmd)
			if got != tc.want {
				t.Errorf("Dispatch(%+v) = %+v, want %+v", tc.cmd, got, tc.want)
			}
		})
	}
}
