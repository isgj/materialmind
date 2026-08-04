package engine

import (
	"context"
	"sync"
	"time"
)

const (
	completedStreamRetention = 5 * time.Minute
	maxRetainedStreamEvents  = 20_000
	streamSubscriberBuffer   = 1_024
)

type StreamEvent struct {
	Sequence int64  `json:"sequence"`
	Type     string `json:"type"`
	Data     any    `json:"data,omitempty"`
}

type stream struct {
	events         []StreamEvent
	eventStart     int
	nextSequence   int64
	droppedThrough int64
	subscribers    map[chan StreamEvent]struct{}
	done           bool
}

type Hub struct {
	mu               sync.Mutex
	streams          map[string]*stream
	retention        time.Duration
	eventLimit       int
	subscriberBuffer int
}

type ReplaySubscription struct {
	Events         <-chan StreamEvent
	Truncated      bool
	OldestSequence int64
}

func NewHub() *Hub {
	return newHub(completedStreamRetention)
}

func newHub(retention time.Duration) *Hub {
	return newHubWithLimits(retention, maxRetainedStreamEvents, streamSubscriberBuffer)
}

func newHubWithLimits(retention time.Duration, eventLimit, subscriberBuffer int) *Hub {
	return &Hub{
		streams:          make(map[string]*stream),
		retention:        retention,
		eventLimit:       max(eventLimit, 1),
		subscriberBuffer: max(subscriberBuffer, 1),
	}
}

func (h *Hub) Create(runID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.streams[runID] = &stream{subscribers: make(map[chan StreamEvent]struct{})}
}

func (h *Hub) Publish(runID, eventType string, data any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	current := h.streams[runID]
	if current == nil || current.done {
		return
	}
	current.nextSequence++
	event := StreamEvent{Sequence: current.nextSequence, Type: eventType, Data: data}
	if len(current.events) < h.eventLimit {
		current.events = append(current.events, event)
	} else {
		current.droppedThrough = current.events[current.eventStart].Sequence
		current.events[current.eventStart] = event
		current.eventStart = (current.eventStart + 1) % len(current.events)
	}
	for subscriber := range current.subscribers {
		select {
		case subscriber <- event:
		default:
			delete(current.subscribers, subscriber)
			close(subscriber)
		}
	}
}

func (h *Hub) Complete(runID string) {
	h.mu.Lock()
	current := h.streams[runID]
	if current == nil || current.done {
		h.mu.Unlock()
		return
	}
	current.done = true
	for subscriber := range current.subscribers {
		close(subscriber)
	}
	clear(current.subscribers)
	h.mu.Unlock()

	time.AfterFunc(h.retention, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.streams[runID] == current && current.done {
			delete(h.streams, runID)
		}
	})
}

func (h *Hub) Subscribe(ctx context.Context, runID string, after int64) (<-chan StreamEvent, bool) {
	subscription, ok := h.SubscribeReplay(ctx, runID, after)
	return subscription.Events, ok
}

func (h *Hub) SubscribeReplay(
	ctx context.Context,
	runID string,
	after int64,
) (ReplaySubscription, bool) {
	h.mu.Lock()
	current := h.streams[runID]
	if current == nil {
		h.mu.Unlock()
		return ReplaySubscription{}, false
	}
	replay := current.eventsAfter(after)
	buffer := len(replay) + h.subscriberBuffer
	channel := make(chan StreamEvent, buffer)
	for _, event := range replay {
		channel <- event
	}
	oldestSequence := current.nextSequence + 1
	if len(current.events) > 0 {
		oldestSequence = current.events[current.eventStart].Sequence
	}
	subscription := ReplaySubscription{
		Events:         channel,
		Truncated:      after > 0 && after < current.droppedThrough,
		OldestSequence: oldestSequence,
	}
	if current.done {
		close(channel)
		h.mu.Unlock()
		return subscription, true
	}
	current.subscribers[channel] = struct{}{}
	h.mu.Unlock()

	go func() {
		<-ctx.Done()
		h.mu.Lock()
		defer h.mu.Unlock()
		current := h.streams[runID]
		if current == nil || current.done {
			return
		}
		if _, ok := current.subscribers[channel]; ok {
			delete(current.subscribers, channel)
			close(channel)
		}
	}()
	return subscription, true
}

func (s *stream) eventsAfter(after int64) []StreamEvent {
	events := make([]StreamEvent, 0, len(s.events))
	for offset := range len(s.events) {
		event := s.events[(s.eventStart+offset)%len(s.events)]
		if event.Sequence > after {
			events = append(events, event)
		}
	}
	return events
}
