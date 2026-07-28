package proxy

import (
	"sync"
	"testing"
	"time"
)

// countingCloser records whether Close was called, signalling via a channel so
// tests observe the watchdog firing without racy polling.
type countingCloser struct {
	closed chan struct{}
	once   sync.Once
}

func newCountingCloser() *countingCloser { return &countingCloser{closed: make(chan struct{})} }

func (c *countingCloser) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

// TestIdleWatchdogFiresOnSilence: with no reset, the watchdog closes the body
// once the idle window elapses - the stall detection that bounds a hung stream.
func TestIdleWatchdogFiresOnSilence(t *testing.T) {
	body := newCountingCloser()
	_, stop, timedOut := armIdleWatchdog(body, 40*time.Millisecond)
	defer stop()

	select {
	case <-body.closed:
		if !timedOut() {
			t.Fatal("watchdog closed the body without recording the timeout")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog never closed the body after the idle window elapsed")
	}
}

// TestIdleWatchdogResetKeepsAlive: repeated reset() calls (as the relay makes
// after every read - including ": keep-alive" comment and empty lines) keep the
// body open past the raw idle window. This is the liveness contract keep-alive
// traffic relies on.
func TestIdleWatchdogResetKeepsAlive(t *testing.T) {
	body := newCountingCloser()
	reset, stop, _ := armIdleWatchdog(body, 60*time.Millisecond)
	defer stop()

	// Reset every 20ms for 100ms total - each "keep-alive" pushes the deadline
	// out, so the body must NOT close during this window despite it exceeding 60ms.
	for i := 0; i < 5; i++ {
		time.Sleep(20 * time.Millisecond)
		reset()
		select {
		case <-body.closed:
			t.Fatalf("watchdog closed the body despite a reset at iteration %d", i)
		default:
		}
	}
}

// TestIdleWatchdogStopPreventsClose: once the read loop exits normally and calls
// stop(), a later deadline must not close an already-finished body.
func TestIdleWatchdogStopPreventsClose(t *testing.T) {
	body := newCountingCloser()
	_, stop, _ := armIdleWatchdog(body, 30*time.Millisecond)
	stop()

	select {
	case <-body.closed:
		t.Fatal("watchdog closed the body after stop()")
	case <-time.After(120 * time.Millisecond):
		// stayed open as expected
	}
}
