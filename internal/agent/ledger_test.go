package agent

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// TestLedgerTotals checks what a join, a start, a leave and a
// visitor's request each add to the totals: the count of joins and the
// peak, the start and its boot time, on the leave the node's lifetime,
// the pods it took with it and the record for the longest-lived node,
// and for the request a count in all, a count for the Service it was
// sent to, and a record of how long its answer took.
func TestLedgerTotals(t *testing.T) {
	now := time.Unix(1000, 0)
	book := newLedger(t.TempDir(), now)

	book.joined("pixel-1", 1, now)
	book.joined("pixel-2", 2, now)
	book.started("pixel-1", "default/web", "Android", "Pixel-9", 2500*time.Millisecond, now)
	book.left("pixel-1", 90*time.Second, []string{"default/web"}, now)
	book.left("pixel-2", 30*time.Second, nil, now)
	book.carried(4096)
	book.asked("counter", 120*time.Millisecond, http.StatusOK, now)
	book.asked("counter", 80*time.Millisecond, http.StatusOK, now)
	book.asked("picture", 40*time.Millisecond, http.StatusBadGateway, now)

	totals, events, boots, asks := book.view()
	if totals.Joins != 2 || totals.PeakNodes != 2 {
		t.Errorf("joins %d, peak %d", totals.Joins, totals.PeakNodes)
	}
	if totals.PodsStarted != 1 || totals.BootSeconds != 2.5 || totals.PodsMoved != 1 {
		t.Errorf("started %d, boot seconds %v, moved %d", totals.PodsStarted, totals.BootSeconds, totals.PodsMoved)
	}
	if totals.NodeSeconds != 120 || totals.LongestName != "pixel-1" || totals.LongestSeconds != 90 {
		t.Errorf("node seconds %v, longest %s for %v", totals.NodeSeconds, totals.LongestName, totals.LongestSeconds)
	}
	if totals.Bytes != 4096 || totals.Asked != 3 {
		t.Errorf("bytes %d, asked %d", totals.Bytes, totals.Asked)
	}
	if totals.AskedOf["counter"] != 2 || totals.AskedOf["picture"] != 1 {
		t.Errorf("asked of each Service: %v", totals.AskedOf)
	}

	// What a reader was given is the reader's own: a later request
	// does not change the count under it.
	book.asked("counter", 60*time.Millisecond, http.StatusOK, now)
	if totals.AskedOf["counter"] != 2 {
		t.Errorf("a view's count changed after the view was taken: %v", totals.AskedOf)
	}
	if !totals.Since.Equal(now) {
		t.Errorf("since %v", totals.Since)
	}

	// The feed, in order: two joins, a start, a leave with its move, a
	// leave.
	kinds := ""
	for index, event := range events {
		kinds += event.Kind + " "
		if event.ID != int64(index+1) {
			t.Errorf("event %d has id %d", index, event.ID)
		}
	}
	if kinds != "join join start leave move leave " {
		t.Errorf("events: %s", kinds)
	}
	if len(boots) != 1 || boots[0].Model != "Pixel-9" || boots[0].Seconds != 2.5 {
		t.Errorf("boots %+v", boots)
	}
	want := []Ask{
		{At: now, App: "counter", Seconds: 0.12, Status: http.StatusOK},
		{At: now, App: "counter", Seconds: 0.08, Status: http.StatusOK},
		{At: now, App: "picture", Seconds: 0.04, Status: http.StatusBadGateway},
	}
	if !slices.Equal(asks, want) {
		t.Errorf("asks %+v", asks)
	}
}

// TestLedgerSaves checks that the totals, the boots and the asks a
// ledger saved are what the next ledger starts with, the day the
// cluster first started and the requests sent to each Service among
// them, and that the feed starts empty: what happened before a restart
// is not happening now.
func TestLedgerSaves(t *testing.T) {
	dir := t.TempDir()
	first := time.Unix(1000, 0)
	book := newLedger(dir, first)
	book.joined("pixel-1", 1, first)
	book.started("pixel-1", "default/web", "Android", "Pixel-9", 2500*time.Millisecond, first)
	book.asked("counter", 120*time.Millisecond, http.StatusOK, first)
	if err := book.save(dir); err != nil {
		t.Fatal(err)
	}

	later := newLedger(dir, time.Unix(9000, 0))
	totals, events, boots, asks := later.view()
	if totals.Joins != 1 || !totals.Since.Equal(first) {
		t.Errorf("after a restart: joins %d, since %v", totals.Joins, totals.Since)
	}
	if totals.Asked != 1 || totals.AskedOf["counter"] != 1 {
		t.Errorf("after a restart: asked %d, of each Service %v", totals.Asked, totals.AskedOf)
	}
	if len(boots) != 1 || boots[0].Seconds != 2.5 || boots[0].Model != "Pixel-9" {
		t.Errorf("after a restart the boots are %+v", boots)
	}
	if len(asks) != 1 || asks[0].Seconds != 0.12 || asks[0].App != "counter" {
		t.Errorf("after a restart the asks are %+v", asks)
	}
	if len(events) != 0 {
		t.Errorf("after a restart the feed has %d events", len(events))
	}
}

// TestLedgerReadsTotalsAlone checks that a file written before boots
// were saved, which is the totals and nothing around them, still gives
// the next ledger its totals: by writing such a file by hand.
func TestLedgerReadsTotalsAlone(t *testing.T) {
	dir := t.TempDir()
	old := `{"since":"2026-11-26T00:00:00Z","joins":7}`
	if err := os.WriteFile(filepath.Join(dir, ledgerFile), []byte(old), ledgerFileMode); err != nil {
		t.Fatal(err)
	}
	totals, _, boots, _ := newLedger(dir, time.Unix(9000, 0)).view()
	if totals.Joins != 7 || totals.Since.Year() != 2026 || len(boots) != 0 {
		t.Errorf("from a file of totals alone: %+v, %d boots", totals, len(boots))
	}
}

// TestLedgerKeepsTheLatest checks that the feed, the boots and the
// asks hold no more than they are meant to, and that what they hold is
// the newest: by recording more of each than the ledger keeps. The
// asks of one Service are kept apart from another's, so that a Service
// a page reads every second does not push out the asks of one a
// visitor sends a request to now and then.
func TestLedgerKeepsTheLatest(t *testing.T) {
	now := time.Unix(1000, 0)
	book := newLedger(t.TempDir(), now)
	for index := 0; index < bootsKept+50; index++ {
		book.started("node", "default/pod", "Android", "Pixel", time.Duration(index)*time.Second, now)
	}

	for index := 0; index < asksKept+50; index++ {
		book.asked("counter", time.Duration(index)*time.Second, http.StatusOK, now)
	}

	for index := 0; index < asksKept+50; index++ {
		book.asked("picture", time.Millisecond, http.StatusOK, now)
	}

	_, events, boots, asks := book.view()
	counter := slices.DeleteFunc(slices.Clone(asks), func(ask Ask) bool { return ask.App != "counter" })
	if len(events) != eventsKept || len(boots) != bootsKept || len(counter) != asksKept || len(asks) != 2*asksKept {
		t.Fatalf("%d events, %d boots, %d asks of the counter and %d asks in all kept", len(events), len(boots), len(counter), len(asks))
	}
	if last := counter[len(counter)-1].Seconds; last != float64(asksKept+49) {
		t.Errorf("the newest ask of the counter kept is %v", last)
	}
	if last := boots[len(boots)-1].Seconds; last != float64(bootsKept+49) {
		t.Errorf("the newest boot kept is %v", last)
	}
	if last := events[len(events)-1].ID; last != int64(bootsKept+50) {
		t.Errorf("the newest event kept is %d", last)
	}
}
