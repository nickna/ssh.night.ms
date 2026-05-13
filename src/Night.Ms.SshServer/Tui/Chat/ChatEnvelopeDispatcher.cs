using System.Text.Json;
using Night.Ms.SshServer.Realtime;

namespace Night.Ms.SshServer.Tui.Chat;

// Parses a Redis chat envelope and routes it to the appropriate handler. Shared by
// ChatScreen and ChatThreadScreen — the two screens previously hand-copied the same switch
// + TryDeserialize boilerplate around their per-event handlers.
//
// Topic events are channel-scoped and ignored by the thread view (it leaves OnTopic null).
// Any handler may be left null; missing handlers cause the event to be dropped silently.
internal sealed class ChatEnvelopeDispatcher
{
    public Action<ChatMessageDto>? OnMessage { get; init; }
    public Action<ChatEditDto>? OnEdit { get; init; }
    public Action<ChatDeleteDto>? OnDelete { get; init; }
    // bool = true → React (add), false → Unreact (remove). One handler with a flag is
    // simpler than two near-identical OnReact/OnUnreact properties.
    public Action<ChatReactionDto, bool>? OnReaction { get; init; }
    public Action<ChatPinDto>? OnPin { get; init; }
    public Action<ChatTopicDto>? OnTopic { get; init; }

    public void Dispatch(byte[] payload)
    {
        ChatEnvelope? envelope;
        try { envelope = JsonSerializer.Deserialize<ChatEnvelope>(payload); }
        catch { return; }
        if (envelope is null) return;

        switch (envelope.Kind)
        {
            case ChatEventKind.Message:
                if (TryDeserialize<ChatMessageDto>(envelope.Payload, out var msg))
                    OnMessage?.Invoke(msg);
                return;
            case ChatEventKind.Edit:
                if (TryDeserialize<ChatEditDto>(envelope.Payload, out var edit))
                    OnEdit?.Invoke(edit);
                return;
            case ChatEventKind.Delete:
                if (TryDeserialize<ChatDeleteDto>(envelope.Payload, out var del))
                    OnDelete?.Invoke(del);
                return;
            case ChatEventKind.React:
                if (TryDeserialize<ChatReactionDto>(envelope.Payload, out var react))
                    OnReaction?.Invoke(react, true);
                return;
            case ChatEventKind.Unreact:
                if (TryDeserialize<ChatReactionDto>(envelope.Payload, out var unreact))
                    OnReaction?.Invoke(unreact, false);
                return;
            case ChatEventKind.Pin:
            case ChatEventKind.Unpin:
                if (TryDeserialize<ChatPinDto>(envelope.Payload, out var pin))
                    OnPin?.Invoke(pin);
                return;
            case ChatEventKind.Topic:
                if (TryDeserialize<ChatTopicDto>(envelope.Payload, out var topicEvt))
                    OnTopic?.Invoke(topicEvt);
                return;
        }
    }

    private static bool TryDeserialize<T>(JsonElement element, out T result) where T : class
    {
        try
        {
            var r = element.Deserialize<T>();
            result = r!;
            return r is not null;
        }
        catch
        {
            result = null!;
            return false;
        }
    }
}
