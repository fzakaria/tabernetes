package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLedgerTotals checks what a join, a start and a leave each add to
// the totals: the count of joins and the peak, the start and its boot
// time, and on the leave the node's lifetime, the pods it took with it
// and the record for the longest-lived node.
func TestLedgerTotals(t *testing.T) {
	now := time.Unix(1000, 0)
	book := newLedger(t.TempDir(), now)

	book.joined("pixel-1", 1, now)
	book.joined("pixel-2", 2, now)
	book.started("pixel-1", "default/web", "Android", "Pixel-9", 2500*time.Millisecond, now)
	book.left("pixel-1", 90*time.Second, []string{"default/web"}, now)
	book.left("pixel-2", 30*time.Second, nil, now)
	book.carried(4096)
	book.asked()

	totals, events, boots := book.view()
	if totals.Joins != 2 || totals.PeakNodes != 2 {
		t.Errorf("joins %d, peak %d", totals.Joins, totals.PeakNodes)
	}
	if totals.PodsStarted != 1 || totals.BootSeconds != 2.5 || totals.PodsMoved != 1 {
		t.Errorf("started %d, boot seconds %v, moved %d", totals.PodsStarted, totals.BootSeconds, totals.PodsMoved)
	}
	if totals.NodeSeconds != 120 || totals.LongestName != "pixel-1" || totals.LongestSeconds != 90 {
		t.Errorf("node seconds %v, longest %s for %v", totals.NodeSeconds, totals.LongestName, totals.LongestSeconds)
	}
	if totals.Bytes != 4096 || totals.Asked != 1 {
		t.Errorf("bytes %d, asked %d", totals.Bytes, totals.Asked)
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
}

// TestLedgerSaves checks that the totals and the boots a ledger saved
// are what the next ledger starts with, the day the cluster first
// started among them, and that the feed starts empty: what happened
// before a restart is not happening now.
func TestLedgerSaves(t *testing.T) {
	dir := t.TempDir()
	first := time.Unix(1000, 0)
	book := newLedger(dir, first)
	book.joined("pixel-1", 1, first)
	book.started("pixel-1", "default/web", "Android", "Pixel-9", 2500*time.Millisecond, first)
	if err := book.save(dir); err != nil {
		t.Fatal(err)
	}

	later := newLedger(dir, time.Unix(9000, 0))
	totals, events, boots := later.view()
	if totals.Joins != 1 || !totals.Since.Equal(first) {
		t.Errorf("after a restart: joins %d, since %v", totals.Joins, totals.Since)
	}
	if len(boots) != 1 || boots[0].Seconds != 2.5 || boots[0].Model != "Pixel-9" {
		t.Errorf("after a restart the boots are %+v", boots)
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
	totals, _, boots := newLedger(dir, time.Unix(9000, 0)).view()
	if totals.Joins != 7 || totals.Since.Year() != 2026 || len(boots) != 0 {
		t.Errorf("from a file of totals alone: %+v, %d boots", totals, len(boots))
	}
}

// TestLedgerKeepsTheLatest checks that the feed and the boots hold no
// more than they are meant to, and that what they hold is the newest.
func TestLedgerKeepsTheLatest(t *testing.T) {
	now := time.Unix(1000, 0)
	book := newLedger(t.TempDir(), now)
	for index := 0; index < bootsKept+50; index++ {
		book.started("node", "default/pod", "Android", "Pixel", time.Duration(index)*time.Second, now)
	}

	_, events, boots := book.view()
	if len(events) != eventsKept || len(boots) != bootsKept {
		t.Errorf("%d events and %d boots kept", len(events), len(boots))
	}
	if last := boots[len(boots)-1].Seconds; last != float64(bootsKept+49) {
		t.Errorf("the newest boot kept is %v", last)
	}
	if last := events[len(events)-1].ID; last != int64(bootsKept+50) {
		t.Errorf("the newest event kept is %d", last)
	}
}
