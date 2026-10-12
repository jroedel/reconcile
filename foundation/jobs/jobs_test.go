package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func newRunner(t *testing.T) *Runner {
	t.Helper()

	return New(t.Context(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// held is work that says how far it got and then waits to be let go.
func held(release <-chan struct{}, result any) Work {
	return func(ctx context.Context, report func(Progress)) (any, error) {
		report(Progress{Stage: "reading", Done: 1, Of: 3})

		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}

		return result, nil
	}
}

func TestAJobFromStartToEnd(t *testing.T) {
	r := newRunner(t)
	release := make(chan struct{})

	id, err := r.Start("ann", "read a,b,c", held(release, "rows"))
	if err != nil {
		t.Fatal(err)
	}

	// Not finished yet: as far as it got, after the wait.
	st, ok := r.Wait(t.Context(), "ann", id, 50*time.Millisecond)
	if !ok || st.Finished || st.Progress != (Progress{Stage: "reading", Done: 1, Of: 3}) {
		t.Errorf("while working: %+v, %v", st, ok)
	}

	// The same work asked for again is the same job; another key is not.
	if again, _ := r.Start("ann", "read a,b,c", held(release, "other")); again != id {
		t.Errorf("asked twice, two jobs: %s and %s", id, again)
	}

	// Somebody else's job is not there for them.
	if _, ok := r.Wait(t.Context(), "bob", id, time.Millisecond); ok {
		t.Error("bob waited on ann's job")
	}

	close(release)

	st, ok = r.Wait(t.Context(), "ann", id, time.Second)
	if !ok || !st.Finished || st.Result != "rows" || st.Err != nil {
		t.Errorf("finished: %+v, %v", st, ok)
	}

	// Once it has finished, the same key starts the work again.
	if again, _ := r.Start("ann", "read a,b,c", held(release, "rows")); again == id {
		t.Error("a finished job was answered for new work")
	}

	if _, ok := r.Wait(t.Context(), "ann", "no such job", time.Millisecond); ok {
		t.Error("a job that never was")
	}
}

func TestAFullLine(t *testing.T) {
	r := newRunner(t)
	release := make(chan struct{})
	defer close(release)

	// One running, Line waiting, and one more refused.
	for i := range Line + 1 {
		if _, err := r.Start("ann", string(rune('a'+i)), held(release, nil)); err != nil {
			t.Fatalf("job %d: %v", i, err)
		}

		if i == 0 {
			// Let the first leave the line before filling it.
			for st, _ := r.Wait(t.Context(), "ann", r.only(t), time.Millisecond); st.Progress.Of == 0; {
				st, _ = r.Wait(t.Context(), "ann", r.only(t), 10*time.Millisecond)
			}
		}
	}

	if _, err := r.Start("ann", "one too many", held(release, nil)); !errors.Is(err, ErrBusy) {
		t.Errorf("one too many: %v", err)
	}
}

// only is the id of the runner's one job.
func (r *Runner) only(t *testing.T) string {
	t.Helper()

	r.mu.Lock()
	defer r.mu.Unlock()

	for id := range r.jobs {
		return id
	}

	t.Fatal("no job")

	return ""
}

func TestAPanicIsTheJobsError(t *testing.T) {
	r := newRunner(t)

	id, _ := r.Start("ann", "k", func(context.Context, func(Progress)) (any, error) { panic("a reader's bug") })

	st, ok := r.Wait(t.Context(), "ann", id, time.Second)
	if !ok || !st.Finished || st.Err == nil {
		t.Errorf("after a panic: %+v", st)
	}

	// And the runner carries on.
	id, _ = r.Start("ann", "k2", func(context.Context, func(Progress)) (any, error) { return 1, nil })
	if st, _ := r.Wait(t.Context(), "ann", id, time.Second); st.Result != 1 {
		t.Errorf("after a panic, the next: %+v", st)
	}
}

func TestAFinishedJobIsForgottenInTime(t *testing.T) {
	r := newRunner(t)

	clock := time.Now()
	r.mu.Lock()
	r.now = func() time.Time { return clock }
	r.mu.Unlock()

	id, _ := r.Start("ann", "k", func(context.Context, func(Progress)) (any, error) { return nil, nil })
	if st, _ := r.Wait(t.Context(), "ann", id, time.Second); !st.Finished {
		t.Fatal("not finished")
	}

	r.mu.Lock()
	clock = clock.Add(Keep + time.Minute)
	r.mu.Unlock()

	_, _ = r.Start("ann", "another", func(context.Context, func(Progress)) (any, error) { return nil, nil })

	if _, ok := r.Wait(t.Context(), "ann", id, time.Millisecond); ok {
		t.Error("a job finished long ago is still there")
	}
}

// A request that gives up waiting does not stop the job: that is the point.
func TestARequestGivingUpLeavesTheJob(t *testing.T) {
	r := newRunner(t)
	release := make(chan struct{})

	id, _ := r.Start("ann", "k", held(release, "done"))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if st, ok := r.Wait(ctx, "ann", id, time.Minute); !ok || st.Finished {
		t.Errorf("a cancelled wait: %+v, %v", st, ok)
	}

	close(release)

	if st, _ := r.Wait(t.Context(), "ann", id, time.Second); st.Result != "done" {
		t.Errorf("the job after its request gave up: %+v", st)
	}
}
