// Package machine reads how much the machine the agent runs on has
// left to give: memory, and time on its CPUs. The agent takes another
// tab as a node for as long as the machine has room, and the machine's
// own account of itself is how the agent knows.
package machine

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The files under /proc a reading is made from.
const (
	fileMemory = "meminfo"
	fileCPU    = "stat"
)

// The lines of meminfo a reading keeps, and the unit the kernel writes
// them in.
const (
	keyMemoryTotal     = "MemTotal:"
	keyMemoryAvailable = "MemAvailable:"
	bytesPerKibibyte   = 1024
)

// The line of stat for every CPU together, and where in the line the
// kinds of time are: the first eight numbers are all the time there
// was (the two after them, a guest's, are already inside the first
// two), and the fourth and fifth are the time spent doing nothing and
// waiting for a disk.
const (
	keyAllCPUs      = "cpu"
	cpuFieldsUsed   = 8
	cpuFieldIdle    = 3
	cpuFieldWaiting = 4
)

// Reading is the machine at one moment.
type Reading struct {
	// MemoryTotal is the machine's memory and MemoryAvailable how much
	// of it could still be given out without swapping, in bytes.
	MemoryTotal     int64
	MemoryAvailable int64
	// CPUTotal is the time the machine's CPUs have had since boot,
	// added up over the CPUs, and CPUIdle the part of that time they
	// did nothing in. Both are in the kernel's ticks, and only the
	// difference between two readings means anything.
	CPUTotal uint64
	CPUIdle  uint64
}

// Read returns a reading of the machine from the files under a
// directory, which is /proc anywhere but in a test.
func Read(root string) (Reading, error) {
	var reading Reading

	// The memory.
	memory, err := os.Open(filepath.Join(root, fileMemory))
	if err != nil {
		return reading, err
	}
	defer memory.Close()
	found := 0
	lines := bufio.NewScanner(memory)
	for lines.Scan() {
		fields := strings.Fields(lines.Text())
		if len(fields) < 2 {
			continue
		}
		var into *int64
		switch fields[0] {
		case keyMemoryTotal:
			into = &reading.MemoryTotal
		case keyMemoryAvailable:
			into = &reading.MemoryAvailable
		default:
			continue
		}
		kibibytes, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return reading, fmt.Errorf("%s: %q: %w", fileMemory, lines.Text(), err)
		}
		*into = kibibytes * bytesPerKibibyte
		found++
	}
	if err := lines.Err(); err != nil {
		return reading, err
	}
	if found != 2 {
		return reading, errors.New(fileMemory + " has no " + keyMemoryTotal + " or no " + keyMemoryAvailable)
	}

	// The CPUs' time, from the first line of stat.
	stat, err := os.ReadFile(filepath.Join(root, fileCPU))
	if err != nil {
		return reading, err
	}
	first, _, _ := strings.Cut(string(stat), "\n")
	fields := strings.Fields(first)
	if len(fields) < 1+cpuFieldsUsed || fields[0] != keyAllCPUs {
		return reading, fmt.Errorf("%s does not start with every CPU's time: %q", fileCPU, first)
	}
	for index, field := range fields[1 : 1+cpuFieldsUsed] {
		ticks, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return reading, fmt.Errorf("%s: %q: %w", fileCPU, first, err)
		}
		reading.CPUTotal += ticks
		if index == cpuFieldIdle || index == cpuFieldWaiting {
			reading.CPUIdle += ticks
		}
	}
	return reading, nil
}

// BusySince returns the part of the time between an earlier reading
// and this one that the CPUs spent doing something, from 0 to 1.
func (r Reading) BusySince(earlier Reading) float64 {
	if r.CPUTotal <= earlier.CPUTotal {
		return 0
	}
	total := float64(r.CPUTotal - earlier.CPUTotal)
	idle := float64(r.CPUIdle - earlier.CPUIdle)
	return min(1, max(0, 1-idle/total))
}
