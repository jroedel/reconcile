package vitals

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseStatus(t *testing.T) {
	const status = "Name:\treconcile\nVmPeak:\t 1300000 kB\nVmHWM:\t  204800 kB\nVmRSS:\t   51200 kB\nThreads:\t9\n"

	if rss, peak := parseStatus(status); rss != 50<<20 || peak != 200<<20 {
		t.Errorf("rss %d, peak %d", rss, peak)
	}

	if rss, peak := parseStatus("Name:\tx\n"); rss != -1 || peak != -1 {
		t.Errorf("nothing to read: rss %d, peak %d", rss, peak)
	}
}

func TestParseLimits(t *testing.T) {
	const limits = "Limit                     Soft Limit           Hard Limit           Units     \n" +
		"Max cpu time              3600                 unlimited            seconds   \n" +
		"Max address space         4194304           4194304           bytes     \n" +
		"Max nice priority         0                    0                    \n"

	got := parseLimits(limits)
	if got["Max cpu time"] != "3600/unlimited" || got["Max address space"] != "4194304/4194304" || got["Max nice priority"] != "0/0" || len(got) != 3 {
		t.Errorf("%v", got)
	}
}

func TestParseEvents(t *testing.T) {
	got := parseEvents("low 0\nhigh 12\nmax 3\noom 1\noom_kill 1\n")
	if got["oom_kill"] != "1" || got["max"] != "3" || len(got) != 5 {
		t.Errorf("%v", got)
	}
}

// A reading always has Go's own numbers, whatever the system lets it read.
func TestRead(t *testing.T) {
	r := Read()
	if r.Heap == 0 || r.GoSys == 0 || r.Goroutines == 0 {
		t.Errorf("%+v", r)
	}

	if len(r.Attrs())%2 != 0 || len(Limits())%2 != 0 {
		t.Error("attributes come in pairs")
	}
}

// A record left behind is a run that did not stop properly; Close removes
// it, and nothing written after Close brings it back.
func TestTheBox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.running")

	box, left, err := OpenBox(path)
	if err != nil || left != "" {
		t.Fatalf("a first run: %q, %v", left, err)
	}

	if err := box.Write("first\n"); err != nil {
		t.Fatal(err)
	}

	if err := box.Write("pid=1 at=then in_flight=\"GET /x\"\n"); err != nil {
		t.Fatal(err)
	}

	// Killed here: the next run finds the last record.
	next, left, err := OpenBox(path)
	if err != nil || left != `pid=1 at=then in_flight="GET /x"` {
		t.Fatalf("after a kill: %q, %v", left, err)
	}

	if err := next.Close(); err != nil {
		t.Fatal(err)
	}

	if err := next.Write("too late"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("after a proper stop the record should be gone: %v", err)
	}

	if _, left, _ := OpenBox(path); left != "" {
		t.Errorf("after a proper stop: %q", left)
	}
}
