package machine

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// A machine with 8 GiB of memory, 6 GiB of it still to give out, as
// the kernel writes /proc/meminfo.
const meminfo = `MemTotal:        8388608 kB
MemFree:         1048576 kB
MemAvailable:    6291456 kB
Buffers:           10240 kB
`

// writeProc makes a directory that stands in for /proc, with the given
// first line of stat.
func writeProc(t *testing.T, cpuLine string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"meminfo": meminfo,
		"stat":    cpuLine + "\ncpu0 1 2 3 4 5 6 7 8 0 0\nintr 12345\n",
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestRead checks that a reading has the machine's memory in bytes and
// its CPUs' time split into all of it and the idle part, where waiting
// for a disk counts as idle and a guest's time is not counted twice: by
// reading a stand-in for /proc.
func TestRead(t *testing.T) {
	root := writeProc(t, "cpu  100 20 30 800 50 4 6 10 7 3")
	reading, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if reading.MemoryTotal != 8<<30 || reading.MemoryAvailable != 6<<30 {
		t.Errorf("memory %d of %d", reading.MemoryAvailable, reading.MemoryTotal)
	}
	if reading.CPUTotal != 1020 || reading.CPUIdle != 850 {
		t.Errorf("CPU time %d, idle %d", reading.CPUTotal, reading.CPUIdle)
	}
}

// TestReadWithoutFiles checks that a machine with no /proc gives an
// error and not a reading of zeros, which would read as a machine with
// no memory left: by reading an empty directory.
func TestReadWithoutFiles(t *testing.T) {
	if _, err := Read(t.TempDir()); err == nil {
		t.Error("an empty directory gave a reading")
	}
}

// TestBusySince checks the part of the time between two readings that
// the CPUs were busy: three quarters when 100 ticks passed and 25 of
// them were idle, and nothing when no time passed at all.
func TestBusySince(t *testing.T) {
	earlier := Reading{CPUTotal: 1000, CPUIdle: 900}
	later := Reading{CPUTotal: 1100, CPUIdle: 925}
	if got := later.BusySince(earlier); math.Abs(got-0.75) > 1e-9 {
		t.Errorf("busy %v, want 0.75", got)
	}
	if got := earlier.BusySince(earlier); got != 0 {
		t.Errorf("busy over no time %v, want 0", got)
	}
}
