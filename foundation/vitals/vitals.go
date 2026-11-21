// Package vitals reads how a running process stands -- its memory, the CPU
// it has used, its threads of work and files -- and the limits it runs
// under, and keeps a record of its last moments that survives it.
//
// It exists because a process killed from outside writes nothing. On
// 2026-11-21 the server stopped between one request and the next with no
// line in its log: no "shutting down", no panic, no out-of-memory error,
// though anything it prints goes to the same file. A kill that cannot be
// caught -- by the host's memory or CPU limits, most likely -- leaves only
// what was written before it, so this writes it before: a reading in the
// log every few minutes, the limits once at start, and a small file that
// is rewritten while the process runs and removed when it stops properly.
// A file still there at the next start is a run that did not stop
// properly, and says what it was doing.
//
// Linux only in what it reads (/proc and /sys/fs/cgroup), which is where
// the server runs; elsewhere every reading it cannot make is left out, and
// nothing fails.
package vitals

import (
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Reading is how the process stood at a moment. A size of -1 is one that
// could not be read here.
type Reading struct {
	// RSS is the memory the system counts against the process now, and
	// PeakRSS the most it has ever counted: the peak catches a request that
	// swelled the process and finished between two readings.
	RSS, PeakRSS int64

	// Heap is what Go's live objects hold, and GoSys all Go has asked the
	// system for.
	Heap, GoSys uint64

	// Group is what the system counts against the whole control group the
	// process runs in -- on a shared hosting account, likely every process
	// of the account, the other apps' too -- and GroupMax its limit.
	Group, GroupMax int64

	// CPU is the processor time used since the process started, user and
	// system together: a limit on CPU time is a limit on this.
	CPU time.Duration

	Goroutines int
	Files      int
}

// Read takes a reading now.
func Read() Reading {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	r := Reading{
		Heap:       ms.HeapAlloc,
		GoSys:      ms.Sys,
		Goroutines: runtime.NumGoroutine(),
		RSS:        -1,
		PeakRSS:    -1,
		Group:      -1,
		GroupMax:   -1,
		Files:      -1,
	}

	if b, err := os.ReadFile("/proc/self/status"); err == nil {
		r.RSS, r.PeakRSS = parseStatus(string(b))
	}

	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) == nil {
		r.CPU = time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
	}

	if fds, err := os.ReadDir("/proc/self/fd"); err == nil {
		r.Files = len(fds)
	}

	if dir := cgroupDir(); dir != "" {
		r.Group = readSize(dir + "/memory.current")
		r.GroupMax = readSize(dir + "/memory.max")
	}

	return r
}

// Attrs is the reading as a log line's keys and values, in megabytes and
// seconds, leaving out what could not be read.
func (r Reading) Attrs() []any {
	a := []any{}

	mb := func(key string, n int64) {
		if n >= 0 {
			a = append(a, key, strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64))
		}
	}

	mb("rss_mb", r.RSS)
	mb("peak_rss_mb", r.PeakRSS)
	mb("heap_mb", int64(r.Heap))
	mb("go_sys_mb", int64(r.GoSys))
	mb("group_mb", r.Group)
	mb("group_max_mb", r.GroupMax)

	a = append(a, "cpu_s", strconv.FormatFloat(r.CPU.Seconds(), 'f', 1, 64), "goroutines", r.Goroutines)

	if r.Files >= 0 {
		a = append(a, "files", r.Files)
	}

	return a
}

// parseStatus finds VmRSS and VmHWM, in bytes, in /proc/self/status.
func parseStatus(s string) (rss, peak int64) {
	rss, peak = -1, -1

	for line := range strings.Lines(s) {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}

		f := strings.Fields(value)
		if len(f) != 2 || f[1] != "kB" {
			continue
		}

		n, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			continue
		}

		switch name {
		case "VmRSS":
			rss = n << 10
		case "VmHWM":
			peak = n << 10
		}
	}

	return rss, peak
}

// --- the limits ----------------------------------------------------------------

// limitsShown are the rows of /proc/self/limits that can end a server, under
// the names a log line gives them.
var limitsShown = []struct{ row, key string }{
	{"Max cpu time", "cpu_seconds"},
	{"Max address space", "address_space"},
	{"Max data size", "data"},
	{"Max resident set", "resident_set"},
	{"Max file size", "file_size"},
	{"Max open files", "open_files"},
	{"Max processes", "processes"},
}

// Limits is what the process may use, as a log line's keys and values: the
// rows of /proc/self/limits that can end a server, each as soft/hard, and
// the control group's memory limit and how often it has killed a process
// for passing it, when there is one. Once, at start: a kill at a limit is
// told by the limit it was.
func Limits() []any {
	a := []any{}

	if b, err := os.ReadFile("/proc/self/limits"); err == nil {
		rows := parseLimits(string(b))

		for _, l := range limitsShown {
			if v, ok := rows[l.row]; ok {
				a = append(a, l.key, v)
			}
		}
	}

	if dir := cgroupDir(); dir != "" {
		for _, f := range []string{"memory.max", "memory.high", "memory.swap.max", "pids.max"} {
			if b, err := os.ReadFile(dir + "/" + f); err == nil {
				a = append(a, "group_"+strings.ReplaceAll(f, ".", "_"), strings.TrimSpace(string(b)))
			}
		}

		if b, err := os.ReadFile(dir + "/memory.events"); err == nil {
			events := parseEvents(string(b))

			for _, k := range []string{"max", "oom", "oom_kill"} {
				if v, ok := events[k]; ok {
					a = append(a, "group_"+k+"_events", v)
				}
			}
		}
	}

	return a
}

var columns = regexp.MustCompile(`\s{2,}`)

// parseLimits reads /proc/self/limits into "soft/hard" by row name.
func parseLimits(s string) map[string]string {
	out := map[string]string{}

	for line := range strings.Lines(s) {
		f := columns.Split(strings.TrimSpace(line), -1)
		if len(f) < 3 || f[0] == "Limit" {
			continue
		}

		out[f[0]] = f[1] + "/" + f[2]
	}

	return out
}

// parseEvents reads a cgroup's memory.events: a name and a count a line.
func parseEvents(s string) map[string]string {
	out := map[string]string{}

	for line := range strings.Lines(s) {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			out[k] = v
		}
	}

	return out
}

// cgroupDir is the directory of the cgroup v2 group the process is in, or ""
// when there is none to read.
func cgroupDir() string {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}

	for line := range strings.Lines(string(b)) {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "0::"); ok {
			dir := "/sys/fs/cgroup" + p
			if _, err := os.Stat(dir + "/memory.current"); err == nil {
				return dir
			}
		}
	}

	return ""
}

// readSize reads a cgroup file holding a number of bytes, or "max": -1 for
// no limit or nothing to read.
func readSize(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return -1
	}

	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return -1
	}

	return n
}

// --- the black box -------------------------------------------------------------

// Box is the small file a running process keeps its last record in. It is
// rewritten whole each time, by a rename, so that a kill in the middle of
// writing leaves the record before rather than half of one.
type Box struct {
	mu     sync.Mutex
	path   string
	closed bool
}

// OpenBox starts the box at path. left is the record a run that did not stop
// properly left there, or "" when the last run stopped properly or there
// was none.
func OpenBox(path string) (box *Box, left string, err error) {
	switch b, err := os.ReadFile(path); {
	case err == nil:
		left = strings.TrimSpace(string(b))
	case !os.IsNotExist(err):
		return nil, "", err
	}

	return &Box{path: path}, left, nil
}

// Write replaces the record. After Close it does nothing, so a last write
// racing a proper stop cannot leave a record that says the run did not stop.
// A nil Box keeps nothing.
func (b *Box) Write(record string) error {
	if b == nil {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil
	}

	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(record), 0o600); err != nil {
		return err
	}

	return os.Rename(tmp, b.path)
}

// Close removes the record: the run stopped properly.
func (b *Box) Close() error {
	if b == nil {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.closed = true

	if err := os.Remove(b.path); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}
