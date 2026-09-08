package cmd

import "testing"

// TestQueuedLine pins the wording of the queued line in both shapes: a real
// rank from the server, and the 0/0 the server sends when it has no rank to
// give (not queued, or a backend older than the fields) — which must keep the
// original generic text rather than print "position 0 of 0".
func TestQueuedLine(t *testing.T) {
	cases := []struct {
		name           string
		position, size int
		want           string
	}{
		{"rank reported", 3, 7, "⏸️  Queued, position 3 of 7"},
		{"front of the queue", 1, 1, "⏸️  Queued, position 1 of 1"},
		{"no rank — older backend", 0, 0, "⏸️  Queued — waiting for a build slot"},
		{"size without position", 0, 4, "⏸️  Queued — waiting for a build slot"},
		{"position without size", 2, 0, "⏸️  Queued — waiting for a build slot"},
		{"negative rank is never printed", -1, -1, "⏸️  Queued — waiting for a build slot"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := queuedLine(tc.position, tc.size); got != tc.want {
				t.Errorf("queuedLine(%d, %d) = %q; want %q", tc.position, tc.size, got, tc.want)
			}
		})
	}
}

// TestQueueTrackerPrintsOncePerPosition is the behavioural pin: the deploy
// polls every three seconds, so a queued deployment must print one line per
// change of position and nothing on the polls in between.
func TestQueueTrackerPrintsOncePerPosition(t *testing.T) {
	var q queueTracker
	polls := []struct {
		status         string
		position, size int
		want           string
	}{
		{"queued", 3, 7, "⏸️  Queued, position 3 of 7"},
		{"queued", 3, 7, ""}, // same rank — silent
		{"queued", 3, 7, ""},
		{"queued", 2, 6, "⏸️  Queued, position 2 of 6"}, // moved up — reprint
		{"queued", 2, 6, ""},
		{"queued", 1, 4, "⏸️  Queued, position 1 of 4"},
		{"building", 0, 0, ""}, // the build started; the loop prints its own line
		{"deploying", 0, 0, ""},
		{"live", 0, 0, ""},
	}
	for i, p := range polls {
		if got := q.next(p.status, p.position, p.size); got != p.want {
			t.Errorf("poll %d (%s %d/%d) = %q; want %q", i, p.status, p.position, p.size, got, p.want)
		}
	}
}

// TestQueueTrackerFallbackPrintsOnce covers the older-backend path: every poll
// yields the same generic line, so it must be printed exactly once — the
// pre-queue-position behaviour, unchanged.
func TestQueueTrackerFallbackPrintsOnce(t *testing.T) {
	var q queueTracker
	if got := q.next("queued", 0, 0); got != "⏸️  Queued — waiting for a build slot" {
		t.Fatalf("first queued poll = %q; want the generic waiting line", got)
	}
	for i := 0; i < 5; i++ {
		if got := q.next("queued", 0, 0); got != "" {
			t.Errorf("repeat poll %d = %q; want silence", i, got)
		}
	}
}

// TestQueueTrackerRequeueReprints: leaving the queue clears the tracker, so a
// deployment that is queued again reprints its line instead of staying silent
// on a stale match.
func TestQueueTrackerRequeueReprints(t *testing.T) {
	var q queueTracker
	q.next("queued", 2, 5)
	if got := q.next("building", 0, 0); got != "" {
		t.Fatalf("building poll = %q; want silence", got)
	}
	if got := q.next("queued", 2, 5); got != "⏸️  Queued, position 2 of 5" {
		t.Errorf("re-queued poll = %q; want the line reprinted", got)
	}
}
