// Package jobs runs work that takes longer than a request may, off the
// request: one piece at a time, in this process, keeping what each came to
// for a while, for whoever started it to ask after.
//
// # Why
//
// The proxy in front of the server gives up on a request after about
// fifteen seconds, answers "Bad Gateway", and the request's context is
// cancelled, so that work tied to it stops wherever it had got to. Reading
// forty statements is longer than that on the first reading, and the limit
// is the proxy's, which a .htaccess cannot raise (issue #107). Work given
// to a Runner runs on the server's own context instead, and the request
// that started it waits a few seconds and then answers with a page, or a
// program's answer, that asks again.
//
// # Why in memory
//
// A job and what it came to are kept in this process and nowhere else. A
// restart -- a deploy -- loses one that had not finished, and whoever asks
// after it is told it is not there, and starts it again. That was chosen
// over a table because the work here is safe to start again (an import
// recognizes what it imported before) and because what it comes to is
// whatever Go values the caller wants back, which a table would have to
// write out and read in again for no reader but the next request.
//
// # One at a time
//
// The work this was made for is reading PDFs, and pdftotext runs one at a
// time on the server anyway: two jobs side by side would each take twice as
// long. Jobs wait in line, a few dozen at most, and Start refuses one more
// with ErrBusy.
package jobs

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ErrBusy is Start's answer when the line is full.
var ErrBusy = errors.New("too much work is waiting; try again in a minute")

// Work is a job's work. It says how far it has got through report, as often
// as it likes, and what it came to by returning.
type Work func(ctx context.Context, report func(Progress)) (any, error)

// Progress is how far a job has got: a stage the caller names, and so many
// of so many in it. Zero until the work first says.
type Progress struct {
	Stage    string
	Done, Of int
}

// Status is a job as it stands.
type Status struct {
	ID       string
	Finished bool
	Progress Progress

	// Result and Err are what the work returned, once Finished.
	Result any
	Err    error
}

// Runner holds the jobs. Its zero value is not usable; New makes one.
type Runner struct {
	log  *slog.Logger
	keep time.Duration
	now  func() time.Time

	line chan *job

	mu   sync.Mutex
	jobs map[string]*job
}

type job struct {
	id, owner, key string
	work           Work
	finished       chan struct{}

	// Under the Runner's mutex.
	progress Progress
	result   any
	err      error
	ended    time.Time
}

// Line is how many jobs may wait to start.
const Line = 32

// Keep is how long a finished job is kept for its owner to ask after.
const Keep = 30 * time.Minute

// New starts a Runner, whose jobs run until ctx is done. A job running then
// has its context cancelled, and one waiting never starts.
func New(ctx context.Context, log *slog.Logger) *Runner {
	r := &Runner{log: log, keep: Keep, now: time.Now, line: make(chan *job, Line), jobs: map[string]*job{}}

	go r.work(ctx)

	return r
}

func (r *Runner) work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-r.line:
			r.run(ctx, j)
		}
	}
}

// run runs one job. A panic in it is its error, not the server's end: the
// work is the caller's, and a request's would have been caught the same way.
func (r *Runner) run(ctx context.Context, j *job) {
	var (
		result any
		err    error
	)

	func() {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("the work stopped unexpectedly: %v", p)
				r.log.Error("a job panicked", "job", j.id, "panic", p)
			}
		}()

		result, err = j.work(ctx, func(p Progress) {
			r.mu.Lock()
			j.progress = p
			r.mu.Unlock()
		})
	}()

	r.mu.Lock()
	j.result, j.err, j.ended = result, err, r.now()
	r.mu.Unlock()

	close(j.finished)
}

// Start puts work in line for owner, and answers its id. A job of the same
// owner and key that has not finished is answered instead, so that a page
// loaded twice, or a button pressed twice, starts the work once. Once it
// has finished, the same key starts the work again: what it came to then
// is not what it would come to now.
//
// Owner and key are the caller's words for who asked and what for; this
// package only compares them.
func (r *Runner) Start(owner, key string, work Work) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.prune()

	for _, j := range r.jobs {
		if j.owner == owner && j.key == key && j.ended.IsZero() {
			return j.id, nil
		}
	}

	j := &job{id: rand.Text(), owner: owner, key: key, work: work, finished: make(chan struct{})}

	select {
	case r.line <- j:
	default:
		return "", ErrBusy
	}

	r.jobs[j.id] = j

	return j.id, nil
}

// Wait is the owner's job by id once it has finished, or as it stands after
// d, or when ctx is done, whichever comes first. False when the owner has
// no job by that id: never had, or it was finished long enough ago to be
// forgotten, or the server has restarted since.
func (r *Runner) Wait(ctx context.Context, owner, id string, d time.Duration) (Status, bool) {
	r.mu.Lock()
	j, ok := r.jobs[id]
	r.mu.Unlock()

	if !ok || j.owner != owner {
		return Status{}, false
	}

	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-j.finished:
	case <-t.C:
	case <-ctx.Done():
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	return Status{ID: j.id, Finished: !j.ended.IsZero(), Progress: j.progress, Result: j.result, Err: j.err}, true
}

// prune forgets the jobs that finished longer than keep ago. Under the
// mutex; on every Start, which is often enough, since nothing else adds.
func (r *Runner) prune() {
	for id, j := range r.jobs {
		if !j.ended.IsZero() && r.now().Sub(j.ended) > r.keep {
			delete(r.jobs, id)
		}
	}
}
