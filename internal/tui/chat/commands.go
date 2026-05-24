// Package chat hosts pure-logic helpers used by the chat screen — slash
// command parsing today, runewidth+emoji+highlight tables in later iterations.
// Kept out of internal/tui/screens so it can be unit-tested in isolation.
package chat

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/nickna/ssh.night.ms/internal/tui/nav"
)

// Command is one parsed slash command. Args is everything after the command
// token, with the leading `/cmd ` stripped — the handler is responsible for
// further splitting if it needs typed arguments.
type Command struct {
	Name string
	Args string
}

// Parse takes an input line and returns (cmd, true) if it's a slash command,
// or ("", false) otherwise. Whitespace-only input is treated as not-a-command
// so the caller can decide what to do with it.
func Parse(line string) (Command, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "/") || len(trimmed) < 2 {
		return Command{}, false
	}
	body := trimmed[1:] // drop the leading /
	if i := strings.IndexByte(body, ' '); i >= 0 {
		return Command{Name: strings.ToLower(body[:i]), Args: strings.TrimSpace(body[i+1:])}, true
	}
	return Command{Name: strings.ToLower(body)}, true
}

// Action is what a command wants the chat screen to do. The screen
// switch-cases on the concrete type — closes over the Decision-style sum
// type pattern we already use for auth decisions.
type Action interface{ isCommandAction() }

// Send: treat the rest of the line as a chat message body (for slash commands
// that compile down to a message, like /me).
type Send struct{ Body string }

// Switch: change the active channel to the named channel, joining if missing.
// EnsureJoin controls whether to add the user to channel_members (true for
// /join, false for /switch).
type Switch struct {
	Channel    string
	EnsureJoin bool
}

// Leave: remove membership from the current channel and fall back to #lobby.
type Leave struct{}

// Navigate: bubble a NavigateMsg back to the root model (e.g., /quit logs out).
type Navigate struct{ Target nav.Destination }

// Notice: render the given line in the local log as a system notice. Used by
// /help and other commands that just want to print text without sending it.
type Notice struct{ Text string }

// Unknown: command not recognized; the screen renders an error notice.
type Unknown struct{ Name string }

// EditLast rewrites the user's most recent message in the active channel.
type EditLast struct{ Body string }

// OpenDM opens (creating if necessary) the deterministic DM channel between
// the current user and the named handle, then switches to it.
type OpenDM struct{ Handle string }

// Who asks the screen to look up currently-online handles via the presence
// service and render them as a Notice in the active log.
type Who struct{}

// Reply threads a new message under the user's most-recent message in the
// active channel. If the user has no message to reply to, the screen surfaces
// a notice.
type Reply struct{ Body string }

// React adds an emoji reaction to the most recent message in the active
// channel (any author's, not just the user's own).
type React struct{ Emoji string }

// Unreact removes the user's own reaction with this emoji from the most
// recent message.
type Unreact struct{ Emoji string }

// Thread pins the chat log to a single message and its replies. Arg is the
// numeric message ID; Arg = 0 clears the filter and shows the whole channel.
type Thread struct{ RootID int64 }

// DeleteLast tombstones the user's own most-recent message in the active
// channel. Sysops also have a /del <id> path but only the own-last form is
// wired today.
type DeleteLast struct{}

// Pin sets is_pinned=true on the channel's most-recent message. Open to any
// participant — the /unpin verb reverses it.
type Pin struct{}

// Unpin clears is_pinned on the latest pinned message in the channel.
type Unpin struct{}

// ListPins prints the channel's currently-pinned messages.
type ListPins struct{}

// Topic sets (or clears, when Body is empty) the active channel's topic.
type Topic struct{ Body string }

// Search runs a full-text search against the active channel's history and
// renders hits as notices in the log.
type Search struct{ Term string }

// Finger asks the chat screen to render a user profile card for `Handle`.
type Finger struct{ Handle string }

func (Send) isCommandAction()       {}
func (Switch) isCommandAction()     {}
func (Leave) isCommandAction()      {}
func (Navigate) isCommandAction()   {}
func (Notice) isCommandAction()     {}
func (Unknown) isCommandAction()    {}
func (EditLast) isCommandAction()   {}
func (OpenDM) isCommandAction()     {}
func (Who) isCommandAction()        {}
func (Reply) isCommandAction()      {}
func (React) isCommandAction()      {}
func (Unreact) isCommandAction()    {}
func (Thread) isCommandAction()     {}
func (DeleteLast) isCommandAction() {}
func (Pin) isCommandAction()        {}
func (Unpin) isCommandAction()      {}
func (ListPins) isCommandAction()   {}
func (Topic) isCommandAction()      {}
func (Search) isCommandAction()     {}
func (Finger) isCommandAction()     {}

// Dispatch maps a parsed Command to an Action. See helpText below for the
// full user-facing surface. Unknown commands return Unknown{Name: ...} so
// the screen can hint.
func Dispatch(c Command) Action {
	switch c.Name {
	case "help", "?":
		return Notice{Text: helpText()}
	case "quit", "logout", "exit":
		return Navigate{Target: nav.DestLogout}
	case "lobby":
		return Navigate{Target: nav.DestLobby}
	case "join":
		name := normalizeChannelName(c.Args)
		if name == "" {
			return Notice{Text: "usage: /join #channel-name"}
		}
		return Switch{Channel: name, EnsureJoin: true}
	case "switch", "go":
		name := normalizeChannelName(c.Args)
		if name == "" {
			return Notice{Text: "usage: /switch #channel-name"}
		}
		return Switch{Channel: name, EnsureJoin: false}
	case "leave", "part":
		return Leave{}
	case "edit":
		if c.Args == "" {
			return Notice{Text: "usage: /edit <new message body>"}
		}
		return EditLast{Body: c.Args}
	case "dm", "msg":
		handle := normalizeHandle(c.Args)
		if handle == "" {
			return Notice{Text: "usage: /dm @handle"}
		}
		return OpenDM{Handle: handle}
	case "me":
		if c.Args == "" {
			return Notice{Text: "usage: /me <action>"}
		}
		// /me is a regular message with a sentinel prefix that ChatLog
		// detects to render as an italic action line. The body persists in
		// chat_messages as-is so any client reading history sees it too.
		return Send{Body: MeMarker + c.Args}
	case "who", "users":
		return Who{}
	case "reply", "r":
		if c.Args == "" {
			return Notice{Text: "usage: /reply <message body>"}
		}
		return Reply{Body: c.Args}
	case "react", "+":
		emoji := strings.TrimSpace(c.Args)
		if emoji == "" {
			return Notice{Text: "usage: /react <emoji>"}
		}
		return React{Emoji: emoji}
	case "unreact", "-":
		emoji := strings.TrimSpace(c.Args)
		if emoji == "" {
			return Notice{Text: "usage: /unreact <emoji>"}
		}
		return Unreact{Emoji: emoji}
	case "thread", "t":
		arg := strings.TrimSpace(c.Args)
		if arg == "" || arg == "0" || arg == "off" || arg == "clear" {
			// /thread with no arg toggles off — clears any active filter.
			return Thread{RootID: 0}
		}
		id, err := strconv.ParseInt(arg, 10, 64)
		if err != nil || id <= 0 {
			return Notice{Text: "usage: /thread <message-id>  (or /thread off to clear)"}
		}
		return Thread{RootID: id}
	case "del", "delete":
		return DeleteLast{}
	case "pin":
		return Pin{}
	case "unpin":
		return Unpin{}
	case "pins":
		return ListPins{}
	case "topic":
		// /topic with no arg clears the topic. Trim once so a wrapping shell
		// quote " topic " behaves like the unquoted form.
		return Topic{Body: strings.TrimSpace(c.Args)}
	case "search", "find":
		term := strings.TrimSpace(c.Args)
		if term == "" {
			return Notice{Text: "usage: /search <term>"}
		}
		return Search{Term: term}
	case "finger":
		handle := normalizeHandle(c.Args)
		if handle == "" {
			return Notice{Text: "usage: /finger @handle"}
		}
		return Finger{Handle: handle}
	}
	return Unknown{Name: c.Name}
}

// MeMarker is the on-wire prefix for /me action messages. ChatLog detects it
// when rendering and switches to italic "* @handle <action>" form. Kept as a
// constant so the producer and consumer can't drift.
const MeMarker = "/me "

// normalizeHandle strips a leading @ and trims whitespace. Users are
// case-insensitive at the DB level (citext) so we don't lowercase here —
// preserving case lets the renderer show the row's canonical handle later.
func normalizeHandle(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "@")
	// Only take the first whitespace-delimited token; anything after a space
	// is ignored for /dm (no inline message body for now).
	if i := strings.IndexByte(s, ' '); i >= 0 {
		s = s[:i]
	}
	return s
}

// normalizeChannelName strips a leading # if present and trims whitespace.
// Channel name validation (length, allowed chars) happens at the service
// layer where we have the schema constraints.
func normalizeChannelName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "#")
	return strings.ToLower(s)
}

func helpText() string {
	return strings.TrimSpace(fmt.Sprintf(`
commands:
  /help              show this list
  /join #name        join (create if needed) and switch to a channel
  /leave             leave the current channel; falls back to #lobby
  /switch #name      switch to a channel without joining
  /edit <body>       rewrite your most recent message in this channel
  /del               delete your most recent message in this channel
  /dm @handle        open a direct message channel with another user
  /me <action>       send an italic action line (e.g., "/me waves")
  /reply <body>      thread under your most-recent message in this channel
  /thread <id>       pin the log to one message and its replies (/thread off to clear)
  /react <emoji>     add a reaction to the latest message in this channel
  /unreact <emoji>   remove your reaction from the latest message
  /pin               pin the latest message in this channel
  /unpin             unpin the latest message in this channel
  /pins              list pinned messages in this channel
  /topic <text>      set this channel's topic (omit text to clear)
  /search <term>     search this channel's history
  /finger @handle    show a user's profile card
  /who               list currently-online users
  /lobby             return to the lobby screen
  /quit              log out of the BBS

press Esc to return to the lobby; PgUp/PgDn to scroll history.
`))
}
