package realtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// publishEvent is a small helper for the ChatService subscription tests —
// the production Publish path is buried inside the mutation methods, but
// from a subscriber's perspective all that matters is that bytes arriving
// on the topic decode into a ChatEvent.
func publishEvent(t *testing.T, bus *fakeBus, ev ChatEvent) {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := bus.Publish(context.Background(), T.ChatChannel(ev.ChannelID), b); err != nil {
		t.Fatalf("publish: %v", err)
	}
}

func TestSubscribeChannelDecodesEvent(t *testing.T) {
	bus := newFakeBus()
	svc := &ChatService{Bus: bus, Logger: quietLogger()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := svc.SubscribeChannel(ctx, 42)
	if err != nil {
		t.Fatalf("SubscribeChannel: %v", err)
	}
	waitFor(t, func() bool { return bus.SubscriberCount(T.ChatChannel(42)) == 1 })

	publishEvent(t, bus, ChatEvent{
		Kind: ChatEventMessageCreated, MessageID: 1, ChannelID: 42,
		UserID: 7, Handle: "alice", Body: "hi",
	})

	select {
	case got := <-stream:
		if got.Kind != ChatEventMessageCreated || got.MessageID != 1 || got.Handle != "alice" {
			t.Errorf("got %+v, want message_created/1/alice", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no event delivered within 1s")
	}
}

func TestSubscribeChannelSkipsMalformedPayload(t *testing.T) {
	bus := newFakeBus()
	svc := &ChatService{Bus: bus, Logger: quietLogger()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := svc.SubscribeChannel(ctx, 7)
	if err != nil {
		t.Fatalf("SubscribeChannel: %v", err)
	}
	waitFor(t, func() bool { return bus.SubscriberCount(T.ChatChannel(7)) == 1 })

	// Garbage first
	if err := bus.Publish(ctx, T.ChatChannel(7), []byte("{not json")); err != nil {
		t.Fatalf("publish bad: %v", err)
	}
	// Then a valid one — the bad payload must not break the stream.
	publishEvent(t, bus, ChatEvent{
		Kind: ChatEventTyping, ChannelID: 7, Handle: "bob",
	})

	select {
	case got := <-stream:
		if got.Kind != ChatEventTyping || got.Handle != "bob" {
			t.Errorf("after malformed: got %+v, want typing/bob", got)
		}
	case <-time.After(time.Second):
		t.Fatal("stream stalled after malformed payload")
	}
}

func TestSubscribeChannelClosesOnCtxCancel(t *testing.T) {
	bus := newFakeBus()
	svc := &ChatService{Bus: bus, Logger: quietLogger()}
	ctx, cancel := context.WithCancel(context.Background())

	stream, err := svc.SubscribeChannel(ctx, 1)
	if err != nil {
		t.Fatalf("SubscribeChannel: %v", err)
	}
	cancel()

	select {
	case _, ok := <-stream:
		if ok {
			t.Errorf("got value on ctx-cancelled stream, want closed channel")
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not close after ctx cancel")
	}
}

func TestSubscribeChannelsEmptyInputReturnsClosedStream(t *testing.T) {
	bus := newFakeBus()
	svc := &ChatService{Bus: bus, Logger: quietLogger()}

	stream, err := svc.SubscribeChannels(context.Background(), nil)
	if err != nil {
		t.Fatalf("SubscribeChannels(nil): %v", err)
	}
	select {
	case _, ok := <-stream:
		if ok {
			t.Errorf("empty SubscribeChannels delivered a value, want immediately closed")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("empty SubscribeChannels did not return a closed channel")
	}
}

func TestSubscribeChannelsFansInAllChannels(t *testing.T) {
	bus := newFakeBus()
	svc := &ChatService{Bus: bus, Logger: quietLogger()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := svc.SubscribeChannels(ctx, []int64{1, 2, 3})
	if err != nil {
		t.Fatalf("SubscribeChannels: %v", err)
	}
	// Wait for all three component subs to register before we publish.
	waitFor(t, func() bool {
		return bus.SubscriberCount(T.ChatChannel(1)) == 1 &&
			bus.SubscriberCount(T.ChatChannel(2)) == 1 &&
			bus.SubscriberCount(T.ChatChannel(3)) == 1
	})

	publishEvent(t, bus, ChatEvent{Kind: ChatEventMessageCreated, ChannelID: 1, MessageID: 11})
	publishEvent(t, bus, ChatEvent{Kind: ChatEventMessageCreated, ChannelID: 2, MessageID: 22})
	publishEvent(t, bus, ChatEvent{Kind: ChatEventMessageCreated, ChannelID: 3, MessageID: 33})

	seen := map[int64]bool{}
	deadline := time.After(2 * time.Second)
	for len(seen) < 3 {
		select {
		case ev := <-stream:
			seen[ev.ChannelID] = true
		case <-deadline:
			t.Fatalf("merged stream only delivered %d/3 channels (seen=%v)", len(seen), seen)
		}
	}
	for _, id := range []int64{1, 2, 3} {
		if !seen[id] {
			t.Errorf("channel %d event not delivered through merged stream", id)
		}
	}
}

func TestSubscribeChannelsClosesAfterCtxCancel(t *testing.T) {
	bus := newFakeBus()
	svc := &ChatService{Bus: bus, Logger: quietLogger()}
	ctx, cancel := context.WithCancel(context.Background())

	stream, err := svc.SubscribeChannels(ctx, []int64{1, 2})
	if err != nil {
		t.Fatalf("SubscribeChannels: %v", err)
	}
	cancel()
	select {
	case _, ok := <-stream:
		if ok {
			t.Errorf("merged stream delivered value after ctx cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("merged stream did not close within 1s of ctx cancel")
	}
}
