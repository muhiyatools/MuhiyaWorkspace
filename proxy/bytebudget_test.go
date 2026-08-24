package proxy

import (
	"context"
	"testing"
	"time"
)

func TestByteBudgetAcquireRelease(t *testing.T) {
	b := newByteBudget(4 << 20) // 4 tokens of 1 MiB
	if err := b.Acquire(context.Background(), 3<<20); err != nil {
		t.Fatalf("expected acquire to succeed: %v", err)
	}
	// Only 1 MiB remains: a 2 MiB request must NOT complete while held.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := b.Acquire(ctx, 2<<20); err == nil {
		t.Fatal("expected acquire to block when budget is exhausted")
	}
	// After release, it succeeds again.
	b.Release(3 << 20)
	if err := b.Acquire(context.Background(), 4<<20); err != nil {
		t.Fatalf("expected acquire to succeed after release: %v", err)
	}
}

func TestByteBudgetCancelReturnsCapacity(t *testing.T) {
	b := newByteBudget(1 << 20)
	go b.Release(0)
	// Fill the bucket, then cancel mid-acquire and verify full capacity is
	// restored (no leaked tokens from the cancelled waiter).
	if err := b.Acquire(context.Background(), 1<<20); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_ = b.Acquire(ctx, 1<<20) // will fail by timeout
	b.Release(1 << 20)
	if err := b.Acquire(context.Background(), 1<<20); err != nil {
		t.Fatalf("capacity leaked after cancelled acquire: %v", err)
	}
}
