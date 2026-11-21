package web

import (
	"cmp"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// InFlight keeps the requests being answered right now.
//
// The request line is written when a request has been answered, so a request
// that takes the process down with it -- killed from outside for the memory
// it made the process use, say -- leaves no line at all, and the log reads as
// though nothing was happening when the server stopped. This is the other
// half: whoever keeps a record of the server's last moments (cmd/reconcile's
// black box) asks it what was being answered.
type InFlight struct {
	mu    sync.Mutex
	now   map[*Flight]struct{}
	dirty atomic.Bool
}

// Flight is one request being answered.
type Flight struct {
	ID     string
	Method string
	Path   string
	Since  time.Time
}

// NewInFlight returns an empty one.
func NewInFlight() *InFlight {
	return &InFlight{now: map[*Flight]struct{}{}}
}

// Track is the middleware that keeps them. It goes inside RequestID, so a
// flight carries the id its request line will. Like Logging, it never keeps
// the query string. A nil InFlight tracks nothing.
func (f *InFlight) Track() Middleware {
	if f == nil {
		return nil
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fl := &Flight{ID: RequestIDFrom(r.Context()), Method: r.Method, Path: r.URL.Path, Since: time.Now()}

			f.mu.Lock()
			f.now[fl] = struct{}{}
			f.mu.Unlock()
			f.dirty.Store(true)

			defer func() {
				f.mu.Lock()
				delete(f.now, fl)
				f.mu.Unlock()
				f.dirty.Store(true)
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// Now is the requests being answered, the longest-running first.
func (f *InFlight) Now() []Flight {
	f.mu.Lock()
	out := make([]Flight, 0, len(f.now))
	for fl := range f.now {
		out = append(out, *fl)
	}
	f.mu.Unlock()

	slices.SortFunc(out, func(x, y Flight) int { return cmp.Compare(x.Since.UnixNano(), y.Since.UnixNano()) })

	return out
}

// Changed reports whether a request began or ended since it was last asked,
// so that a record of them is rewritten when there is something new to say
// and not otherwise.
func (f *InFlight) Changed() bool {
	return f.dirty.Swap(false)
}
