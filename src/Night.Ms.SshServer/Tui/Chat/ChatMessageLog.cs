using Night.Ms.SshServer.Domain;
using Night.Ms.SshServer.Realtime;

namespace Night.Ms.SshServer.Tui.Chat;

// Per-screen state of the chat log: the ordered list of on-screen messages and the
// per-message reaction map. Methods just mutate state and return what changed — the caller
// (screen) decides how to re-render. Shared by ChatScreen (channel view) and ChatThreadScreen
// (thread focus view), which previously hand-copied the same edit/delete/pin/react bodies.
//
// Not thread-safe — callers run all mutations from the dispatcher / UI handlers on a single
// logical thread, matching the previous in-screen field access pattern.
internal sealed class ChatMessageLog
{
    private readonly List<MessageRef> _messages = new();
    private readonly Dictionary<long, MessageRef> _byId = new();
    private readonly Dictionary<long, Dictionary<string, HashSet<long>>> _reactions = new();

    public IReadOnlyList<MessageRef> Messages => _messages;
    public int Count => _messages.Count;

    public MessageRef? Find(long messageId) => _byId.GetValueOrDefault(messageId);

    public bool Contains(long messageId) => _byId.ContainsKey(messageId);

    public void Add(MessageRef msgRef)
    {
        _messages.Add(msgRef);
        _byId[msgRef.MessageId] = msgRef;
    }

    public void Insert(int index, MessageRef msgRef)
    {
        _messages.Insert(index, msgRef);
        _byId[msgRef.MessageId] = msgRef;
    }

    public void Clear()
    {
        _messages.Clear();
        _byId.Clear();
        _reactions.Clear();
    }

    // Apply* return the affected MessageRef (or null if it wasn't in the log) so the caller
    // can repaint its single row. Mutations are idempotent — re-applying the same edit/delete
    // is a no-op beyond returning the ref.
    public MessageRef? ApplyEdit(ChatEditDto edit)
    {
        var msgRef = Find(edit.MessageId);
        if (msgRef is null) return null;
        msgRef.Body = edit.Body;
        msgRef.Edited = true;
        return msgRef;
    }

    public MessageRef? ApplyDelete(ChatDeleteDto del)
    {
        var msgRef = Find(del.MessageId);
        if (msgRef is null) return null;
        msgRef.Deleted = true;
        return msgRef;
    }

    public MessageRef? ApplyPin(ChatPinDto pin)
    {
        var msgRef = Find(pin.MessageId);
        if (msgRef is null) return null;
        msgRef.Pinned = pin.IsPinned;
        return msgRef;
    }

    public MessageRef? BumpReplyCount(long? parentId)
    {
        if (parentId is not { } pid) return null;
        var parent = Find(pid);
        if (parent is null) return null;
        parent.ReplyCount += 1;
        return parent;
    }

    // Reaction state is tracked even for messages no longer in _messages (the caller can
    // filter via Contains before calling if they don't want that). Returns true if the call
    // changed the reaction map for the message — false when removing a reaction that wasn't
    // present.
    public bool ApplyReaction(ChatReactionDto react, bool add)
    {
        if (!_reactions.TryGetValue(react.MessageId, out var map))
        {
            if (!add) return false;
            map = new Dictionary<string, HashSet<long>>();
            _reactions[react.MessageId] = map;
        }
        if (!map.TryGetValue(react.Emoji, out var users))
        {
            if (!add) return false;
            users = new HashSet<long>();
            map[react.Emoji] = users;
        }
        var changed = add ? users.Add(react.UserId) : users.Remove(react.UserId);
        if (users.Count == 0) map.Remove(react.Emoji);
        if (map.Count == 0) _reactions.Remove(react.MessageId);
        return changed;
    }

    public IReadOnlyList<ReactionSummary> BuildSummaries(long messageId, long viewerUserId)
    {
        if (!_reactions.TryGetValue(messageId, out var map) || map.Count == 0)
            return Array.Empty<ReactionSummary>();
        return map.Select(kv => new ReactionSummary(kv.Key, kv.Value.Count, kv.Value.Contains(viewerUserId)))
                  .OrderByDescending(s => s.Count)
                  .ThenBy(s => s.Emoji, StringComparer.Ordinal)
                  .ToArray();
    }

    public void SeedReactions(long messageId, IEnumerable<MessageReaction> rows)
    {
        var map = new Dictionary<string, HashSet<long>>();
        foreach (var r in rows)
        {
            if (!map.TryGetValue(r.Emoji, out var set))
            {
                set = new HashSet<long>();
                map[r.Emoji] = set;
            }
            set.Add(r.UserId);
        }
        if (map.Count > 0) _reactions[messageId] = map;
    }
}
