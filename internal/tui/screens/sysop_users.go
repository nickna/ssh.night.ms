package screens

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// pendingAction is the consolidated state for any confirmation modal on the
// sysop screen. wall set this with kind="wall"; the new commands
// (reset-password, remove-keys, delete-user) all funnel here so the modal
// rendering + key handling stay in one place.
type pendingAction struct {
	kind         string   // "wall" | "reset-password" | "remove-keys" | "delete-user"
	targetHandle string   // canonical handle (case as stored); unused for "wall"
	targetID     int64    // pre-resolved on pre-flight; 0 for "wall"
	wallBody     string   // wall message (only meaningful for kind == "wall")
	summary      []string // pre-rendered impact lines for the modal body
	confirmLabel string   // verb shown next to [Y], e.g. "send", "reset", "remove", "delete"
}

// sysopPreflightMsg arrives back at the screen after the async lookup that
// gathers the impact summary for one of the destructive user-targeted
// commands. err != nil aborts before the modal opens.
type sysopPreflightMsg struct {
	action *pendingAction
	err    error
}

// runPending dispatches the exec Cmd for a previously-confirmed action.
// Called from handleKey after the sysop pressed Y on the modal.
func (m *Sysop) runPending(p *pendingAction) tea.Cmd {
	switch p.kind {
	case "wall":
		return m.wallCmd(p.wallBody)
	case "reset-password":
		return m.resetPasswordCmd(p.targetID, p.targetHandle)
	case "remove-keys":
		return m.removeKeysCmd(p.targetID, p.targetHandle)
	case "delete-user":
		return m.deleteUserCmd(p.targetID, p.targetHandle)
	}
	return done("[!] unknown pending action: "+p.kind, false)
}

// preflightUserCmd resolves the target handle, gathers per-command impact
// context (key count, content counts, online status) and returns the
// summary-populated pendingAction so the screen can render the modal. Each
// kind decides which inputs it actually needs; cheap kinds skip the heavier
// queries.
func (m *Sysop) preflightUserCmd(handle, kind string) tea.Cmd {
	if handle == "" {
		return done("[!] "+kind+" requires a handle.", false)
	}
	queries := m.sess.Queries
	presence := m.sess.Presence
	actor := m.sess.Identity
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		target, err := queries.GetUserByHandle(ctx, handle)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return sysopPreflightMsg{err: fmt.Errorf("no such user: %s", handle)}
			}
			return sysopPreflightMsg{err: err}
		}
		if target.ID == actor.UserID {
			return sysopPreflightMsg{err: errors.New("you can't run that against your own account")}
		}
		action := &pendingAction{
			kind:         kind,
			targetHandle: target.Handle,
			targetID:     target.ID,
		}
		keyCount, _ := queries.CountSshCredentialsForUser(ctx, target.ID)
		online := false
		if presence != nil {
			online, _ = presence.IsOnline(ctx, target.Handle)
		}
		hasPassword := len(target.PasswordHash) > 0

		switch kind {
		case "reset-password":
			action.confirmLabel = "reset"
			pwdNote := ""
			if !hasPassword {
				pwdNote = "  (no current password set — temp will be their first)"
			}
			action.summary = []string{
				fmt.Sprintf("reset password for %s?", target.Handle),
				fmt.Sprintf("  %d SSH key(s) on file · %s", keyCount, onlineLabel(online)),
				"  a 12-char temp password will be shown once after confirmation.",
			}
			if pwdNote != "" {
				action.summary = append(action.summary, pwdNote)
			}

		case "remove-keys":
			action.confirmLabel = "remove"
			if keyCount == 0 {
				return sysopPreflightMsg{err: fmt.Errorf("%s has no SSH keys to remove", target.Handle)}
			}
			lines := []string{
				fmt.Sprintf("remove all SSH keys from %s?", target.Handle),
				fmt.Sprintf("  %d key(s) will be deleted · %s", keyCount, onlineLabel(online)),
			}
			if target.RequireSshKey {
				lines = append(lines, "  WARNING: passwordless mode is on — this locks them out.")
			} else if !hasPassword {
				lines = append(lines, "  WARNING: no password set — only OAuth (if any) will remain.")
			}
			action.summary = lines

		case "delete-user":
			action.confirmLabel = "delete"
			counts, cerr := queries.CountUserContent(ctx, target.ID)
			if cerr != nil {
				return sysopPreflightMsg{err: fmt.Errorf("content count: %w", cerr)}
			}
			lines := []string{
				fmt.Sprintf("DELETE user %s? this cannot be undone.", target.Handle),
				fmt.Sprintf("  %d SSH key(s) · %d chat msg(s) · %d topic(s) · %d post(s)",
					keyCount, counts.ChatCount, counts.TopicCount, counts.PostCount),
				"  history will remain, attributed to [deleted].",
			}
			if online {
				lines = append(lines, "  ONLINE — will be disconnected on confirm.")
			}
			action.summary = lines
		}
		return sysopPreflightMsg{action: action}
	}
}

func onlineLabel(online bool) string {
	if online {
		return "currently online"
	}
	return "offline"
}

// resetPasswordCmd generates a 12-char temp password, hashes it via the
// process Argon2id hasher, persists, and returns the plaintext exactly once
// in the status line. The plaintext is never logged or written into the
// audit row's details — the sysop is expected to relay it out-of-band.
func (m *Sysop) resetPasswordCmd(targetID int64, targetHandle string) tea.Cmd {
	queries := m.sess.Queries
	hasher := m.sess.Hasher
	actorID := m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		if hasher == nil {
			return sysopCmdDoneMsg{status: "[!] reset-password: hasher not configured."}
		}
		temp, err := generateTempPassword(12)
		if err != nil {
			return sysopCmdDoneMsg{status: "[!] reset-password rand: " + err.Error()}
		}
		hash, algo, err := hasher.Hash(temp)
		if err != nil {
			return sysopCmdDoneMsg{status: "[!] reset-password hash: " + err.Error()}
		}
		var algoPtr *string
		if algo != "" {
			a := algo
			algoPtr = &a
		}
		if err := queries.UpdateUserPassword(ctx, gen.UpdateUserPasswordParams{
			ID:                targetID,
			PasswordHash:      hash,
			PasswordAlgo:      algoPtr,
			PasswordUpdatedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		}); err != nil {
			return sysopCmdDoneMsg{status: "[!] reset-password: " + err.Error()}
		}
		_ = writeAudit(ctx, queries, actorID, "user.password.reset_by_sysop", "user", targetID)
		return sysopCmdDoneMsg{
			status: fmt.Sprintf("temp password for %s: %s  (shown once — copy it now)", targetHandle, temp),
			reload: true,
		}
	}
}

// removeKeysCmd deletes every SSH credential for the target user in one
// statement. Audit log records the count in details for posterity.
func (m *Sysop) removeKeysCmd(targetID int64, targetHandle string) tea.Cmd {
	queries := m.sess.Queries
	actorID := m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		n, err := queries.DeleteAllSshCredentialsForUser(ctx, targetID)
		if err != nil {
			return sysopCmdDoneMsg{status: "[!] remove-keys: " + err.Error()}
		}
		details, _ := json.Marshal(map[string]any{"count": n})
		aid := actorID
		tid := targetID
		_ = queries.InsertAuditLog(ctx, gen.InsertAuditLogParams{
			ActorID:    &aid,
			Action:     "user.ssh_keys.cleared_by_sysop",
			TargetType: "user",
			TargetID:   &tid,
			Details:    details,
			CreatedAt:  pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		})
		return sysopCmdDoneMsg{
			status: fmt.Sprintf("removed %d SSH key(s) from %s.", n, targetHandle),
			reload: true,
		}
	}
}

// kickCmd drops every active session for the target user via the
// SessionKicker. No confirmation modal — a kicked user can immediately
// reconnect, so the operation is recoverable in seconds. Records an audit
// entry so post-incident review can spot the action.
func (m *Sysop) kickCmd(handle string) tea.Cmd {
	if handle == "" {
		return done("[!] kick requires a handle.", false)
	}
	queries := m.sess.Queries
	kicker := m.sess.Kicker
	actor := m.sess.Identity
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(5 * time.Second)
		defer cancel()
		target, err := queries.GetUserByHandle(ctx, handle)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return sysopCmdDoneMsg{status: "[!] no such user: " + handle}
			}
			return sysopCmdDoneMsg{status: "[!] lookup failed: " + err.Error()}
		}
		if target.ID == actor.UserID {
			return sysopCmdDoneMsg{status: "[!] you can't kick yourself."}
		}
		if kicker == nil {
			return sysopCmdDoneMsg{status: "[!] kick: session kicker not configured."}
		}
		if err := kicker.Kick(ctx, target.ID); err != nil {
			return sysopCmdDoneMsg{status: "[!] kick: " + err.Error()}
		}
		_ = writeAudit(ctx, queries, actor.UserID, "user.kicked_by_sysop", "user", target.ID)
		return sysopCmdDoneMsg{
			status: fmt.Sprintf("kicked %s — open sessions dropped.", target.Handle),
		}
	}
}

// deleteUserCmd hard-deletes the target user. chat_messages, topics, and
// posts have ON DELETE RESTRICT FKs to users, so the tx re-points each of
// those to the lazy-created '[deleted]' sentinel before DELETE FROM users.
// CASCADE handles the rest (credentials, wallets, reactions, reads, etc.).
// After commit we kick any open sessions so the now-dead user's terminal
// drops instead of reading half-deleted state on its next query.
func (m *Sysop) deleteUserCmd(targetID int64, targetHandle string) tea.Cmd {
	pool := m.sess.Pool
	queries := m.sess.Queries
	kicker := m.sess.Kicker
	actorID := m.sess.Identity.UserID
	return func() tea.Msg {
		ctx, cancel := m.sess.CtxWithTimeout(15 * time.Second)
		defer cancel()
		tx, err := pool.Begin(ctx)
		if err != nil {
			return sysopCmdDoneMsg{status: "[!] delete-user begin tx: " + err.Error()}
		}
		defer tx.Rollback(ctx)
		q := queries.WithTx(tx)
		sentinel, err := q.GetOrCreateSentinelUser(ctx)
		if err != nil {
			return sysopCmdDoneMsg{status: "[!] delete-user sentinel: " + err.Error()}
		}
		if sentinel == targetID {
			return sysopCmdDoneMsg{status: "[!] refusing to delete the [deleted] sentinel itself."}
		}
		if err := q.ReassignChatMessagesAuthor(ctx, gen.ReassignChatMessagesAuthorParams{
			UserID: targetID, UserID_2: sentinel,
		}); err != nil {
			return sysopCmdDoneMsg{status: "[!] reassign chat: " + err.Error()}
		}
		if err := q.ReassignTopicsAuthor(ctx, gen.ReassignTopicsAuthorParams{
			CreatedByID: targetID, CreatedByID_2: sentinel,
		}); err != nil {
			return sysopCmdDoneMsg{status: "[!] reassign topics: " + err.Error()}
		}
		if err := q.ReassignPostsAuthor(ctx, gen.ReassignPostsAuthorParams{
			CreatedByID: targetID, CreatedByID_2: sentinel,
		}); err != nil {
			return sysopCmdDoneMsg{status: "[!] reassign posts: " + err.Error()}
		}
		if err := q.DeleteUserByID(ctx, targetID); err != nil {
			return sysopCmdDoneMsg{status: "[!] delete-user: " + err.Error()}
		}
		if err := tx.Commit(ctx); err != nil {
			return sysopCmdDoneMsg{status: "[!] delete-user commit: " + err.Error()}
		}
		// audit_log.actor_id is ON DELETE SET NULL and target_id has no FK,
		// so this row survives even if a future sysop deletes themselves.
		_ = writeAudit(ctx, queries, actorID, "user.deleted_by_sysop", "user", targetID)
		if kicker != nil {
			_ = kicker.Kick(ctx, targetID)
		}
		return sysopCmdDoneMsg{
			status: fmt.Sprintf("deleted %s — content reassigned to [deleted].", targetHandle),
			reload: true,
		}
	}
}

// generateTempPassword returns n chars drawn uniformly via crypto/rand from
// an unambiguous alphabet (no 0/O/1/l/I) so the sysop can read it back over
// a phone without "is that a one or an ell". 12 chars from a 55-glyph
// alphabet gives ~69 bits of entropy — plenty for a one-time hand-off
// pending the user setting their own.
func generateTempPassword(n int) (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789"
	if n <= 0 {
		return "", errors.New("length must be positive")
	}
	max := big.NewInt(int64(len(alphabet)))
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = alphabet[v.Int64()]
	}
	return string(out), nil
}
