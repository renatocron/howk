//go:build !integration

package broker

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestOffsetTracker_SequentialCompletion verifies that completing offsets in order
// immediately advances the commit cursor to each completed offset.
func TestOffsetTracker_SequentialCompletion(t *testing.T) {
	ot := newOffsetTracker("test-topic", 0, 0)

	markTo, ok := ot.MarkCompleted(0)
	assert.True(t, ok)
	assert.Equal(t, int64(0), markTo)

	markTo, ok = ot.MarkCompleted(1)
	assert.True(t, ok)
	assert.Equal(t, int64(1), markTo)

	markTo, ok = ot.MarkCompleted(2)
	assert.True(t, ok)
	assert.Equal(t, int64(2), markTo)
}

// TestOffsetTracker_OutOfOrderCompletion verifies that completing offsets out of order
// withholds the commit cursor until the contiguous range fills in.
func TestOffsetTracker_OutOfOrderCompletion(t *testing.T) {
	ot := newOffsetTracker("test-topic", 0, 0)

	// Complete offset 2 first — gap at 0 and 1 should prevent any commit.
	markTo, ok := ot.MarkCompleted(2)
	// markTo = lowestPending-1 = -1, which is < 0, so ok=false.
	assert.False(t, ok)
	_ = markTo

	// Complete offset 1 — still a gap at 0.
	markTo, ok = ot.MarkCompleted(1)
	assert.False(t, ok)
	_ = markTo

	// Complete offset 0 — gap closes; contiguous range 0,1,2 becomes committable.
	markTo, ok = ot.MarkCompleted(0)
	assert.True(t, ok)
	assert.Equal(t, int64(2), markTo)
}

// TestOffsetTracker_GapDelaysCommit verifies that a gap in the middle stalls the
// commit cursor at the offset just before the gap.
func TestOffsetTracker_GapDelaysCommit(t *testing.T) {
	ot := newOffsetTracker("test-topic", 0, 0)

	// Complete 0 and 1, then skip 2, complete 3.
	markTo0, ok0 := ot.MarkCompleted(0)
	markTo1, ok1 := ot.MarkCompleted(1)
	// Intentionally skip offset 2.
	markTo3, ok3 := ot.MarkCompleted(3)

	assert.True(t, ok0)
	assert.Equal(t, int64(0), markTo0)
	assert.True(t, ok1)
	assert.Equal(t, int64(1), markTo1)

	// Offset 3 completes but gap at 2 blocks; markTo stays at 1.
	// The loop stops at lowestPending=2 which is not completed, so no advance beyond 1.
	// At the time offset 3 is marked: lowestPending=2, completed[3]=true.
	// The loop tries completed[2] → false → break. markTo = lowestPending-1 = 1.
	assert.True(t, ok3)
	assert.Equal(t, int64(1), markTo3)

	// Now fill the gap at 2 — cursor should jump to 3.
	markTo2, ok2 := ot.MarkCompleted(2)
	assert.True(t, ok2)
	assert.Equal(t, int64(3), markTo2)
}

// TestOffsetTracker_SingleOffset verifies correct behavior with exactly one offset.
func TestOffsetTracker_SingleOffset(t *testing.T) {
	ot := newOffsetTracker("test-topic", 0, 5)

	// lowestPending starts at 5. Complete offset 5.
	markTo, ok := ot.MarkCompleted(5)
	assert.True(t, ok)
	assert.Equal(t, int64(5), markTo)
}

// TestOffsetTracker_EmptyTracker verifies that an empty tracker (no completions)
// returns nothing committable.
func TestOffsetTracker_EmptyTracker(t *testing.T) {
	ot := newOffsetTracker("test-topic", 0, 0)

	// lowestPending=0, no completions. markTo = -1.
	assert.Equal(t, int64(0), ot.lowestPending)
	assert.Equal(t, int64(0), ot.highestReceived)
	assert.Empty(t, ot.completed)
}

// TestOffsetTracker_NonZeroInitialOffset verifies trackers seeded at non-zero offsets
// work identically to those starting at zero. The commit cursor initialises to
// lowestPending-1 (one below the first expected offset), so completing an out-of-order
// offset still returns ok=true but with the "below initial" markTo value.
func TestOffsetTracker_NonZeroInitialOffset(t *testing.T) {
	ot := newOffsetTracker("test-topic", 2, 100)

	// Skip 100, complete 101 first.
	// markTo = lowestPending-1 = 99. Loop: completed[100]=false → break.
	// Returns (99, true) because 99 >= 0. No new contiguous range advanced.
	markTo, ok := ot.MarkCompleted(101)
	assert.True(t, ok)
	assert.Equal(t, int64(99), markTo)

	// Now complete 100 — contiguous 100,101 closes; cursor advances to 101.
	markTo, ok = ot.MarkCompleted(100)
	assert.True(t, ok)
	assert.Equal(t, int64(101), markTo)
}

// TestOffsetTracker_CleansUpCompleted verifies that MarkCompleted removes committed
// offsets from the map (no unbounded memory growth under normal use).
func TestOffsetTracker_CleansUpCompleted(t *testing.T) {
	ot := newOffsetTracker("test-topic", 0, 0)

	ot.MarkCompleted(0)
	ot.MarkCompleted(1)
	ot.MarkCompleted(2)

	// All three offsets should have been processed and removed from the map.
	ot.mu.Lock()
	defer ot.mu.Unlock()
	assert.Empty(t, ot.completed, "completed map should be drained after sequential commits")
	assert.Equal(t, int64(3), ot.lowestPending)
}

// TestOffsetTracker_ConcurrentMarkCompleted verifies that concurrent calls to
// MarkCompleted do not race and produce a consistent lowestPending cursor.
func TestOffsetTracker_ConcurrentMarkCompleted(t *testing.T) {
	const n = 50
	ot := newOffsetTracker("test-topic", 0, 0)

	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ot.MarkCompleted(int64(i))
		}()
	}
	wg.Wait()

	// After all goroutines complete, lowestPending should have advanced to n.
	ot.mu.Lock()
	defer ot.mu.Unlock()
	assert.Equal(t, int64(n), ot.lowestPending)
	assert.Empty(t, ot.completed)
}
