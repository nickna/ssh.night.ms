using Night.Ms.SshServer.Tui.Views;
using Terminal.Gui.Input;

namespace Night.Ms.SshServer.Tui;

// Captures the "press D once to arm, twice to commit" delete pattern used by list-based
// screens. The first D-press records the selected item's id and shows a one-line "press
// D again" warning in the status line; the second D-press on the same item commits.
// Any other key (handled by the caller via Reset) cancels the arming so a stray
// navigation key doesn't leave the screen in a stuck "armed" state.
internal sealed class TwoStepDelete<T> where T : class
{
    private readonly BbsStatusLine _status;
    private readonly Func<T, long> _id;
    private readonly Func<T, string> _label;
    private readonly Action<T> _commit;
    private long? _pendingId;

    public TwoStepDelete(BbsStatusLine status, Func<T, long> id, Func<T, string> label, Action<T> commit)
    {
        _status = status;
        _id = id;
        _label = label;
        _commit = commit;
    }

    public bool IsArmed => _pendingId is not null;

    // Returns true if `key` was a D press (whether arming or committing). The caller
    // marks the key handled and skips its other branches in that case.
    public bool TryHandle(Key key, T? selected)
    {
        if (!key.Matches(Key.D)) return false;
        if (selected is null) return true;
        if (_pendingId == _id(selected))
        {
            _pendingId = null;
            _commit(selected);
        }
        else
        {
            _pendingId = _id(selected);
            _status.SetWarning($"[!] press D again to delete '{_label(selected)}'. Any other key cancels.");
        }
        return true;
    }

    public void Reset() => _pendingId = null;
}
