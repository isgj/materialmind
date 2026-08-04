package mcpruntime

import (
	"context"
	"sync"
	"time"
)

type callTimeoutContextKey struct{}

type callInactivityTimeout struct {
	mu       sync.Mutex
	duration time.Duration
	cancel   context.CancelFunc
	timer    *time.Timer
	pauses   int
	stopped  bool
	timedOut bool
}

func newCallInactivityTimeout(
	parent context.Context,
	duration time.Duration,
) (context.Context, *callInactivityTimeout) {
	ctx, cancel := context.WithCancel(parent)
	timeout := &callInactivityTimeout{
		duration: duration,
		cancel:   cancel,
	}
	timeout.timer = time.AfterFunc(duration, timeout.expire)
	return context.WithValue(ctx, callTimeoutContextKey{}, timeout), timeout
}

func (t *callInactivityTimeout) pause() func() {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return func() {}
	}
	t.pauses++
	t.timer.Stop()
	t.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(t.resume)
	}
}

func (t *callInactivityTimeout) resume() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pauses == 0 {
		return
	}
	t.pauses--
	if t.pauses == 0 && !t.stopped {
		t.timer.Reset(t.duration)
	}
}

func (t *callInactivityTimeout) expire() {
	t.mu.Lock()
	if t.stopped || t.pauses > 0 {
		t.mu.Unlock()
		return
	}
	t.stopped = true
	t.timedOut = true
	cancel := t.cancel
	t.mu.Unlock()
	cancel()
}

func (t *callInactivityTimeout) stop() {
	t.mu.Lock()
	if !t.stopped {
		t.stopped = true
		t.timer.Stop()
	}
	cancel := t.cancel
	t.mu.Unlock()
	cancel()
}

func (t *callInactivityTimeout) didTimeOut() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.timedOut
}
