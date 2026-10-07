// Package web is the HTTP plumbing that has no domain knowledge: the
// middleware every request passes through, the response-header policy, and the
// server that runs it all.
//
// Taken from /opt/projects/stewards, which took it from mass-intentions.
//
// Nothing here knows a domain word. When two apps need the
// same request middleware it belongs here; when they need the same page chrome
// it belongs one layer up in app/sdk/page, because a <head> block is
// app-specific and a request log is not.
package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"slices"
	"time"
)

// Middleware wraps a handler. Nothing more.
type Middleware func(http.Handler) http.Handler

// Wrap applies middleware so that the first argument is the outermost layer,
// which is the order the chain is written down in and therefore the order it
// should be read in.
func Wrap(h http.Handler, mw ...Middleware) http.Handler {
	for _, m := range slices.Backward(mw) {
		if m != nil {
			h = m(h)
		}
	}

	return h
}

// --- request id -------------------------------------------------------------

type ctxKey int

const requestIDKey ctxKey = iota + 1

// RequestIDFrom returns the id every log line for this request carries, or the
// empty string outside a request.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// RequestID mints the id the rest of the chain logs against.
//
// It is the outermost middleware so that the request line and a panic line
// carry the same id, which is the only thing that lets the two be matched up
// afterwards. The id is ours rather than a client-supplied header: a stranger
// choosing the correlation id in our logs can make two unrelated requests look
// like one.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var b [8]byte
			rand.Read(b[:]) // documented never to fail

			ctx := context.WithValue(r.Context(), requestIDKey, hex.EncodeToString(b[:]))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// --- logging ----------------------------------------------------------------

// recorder remembers what was answered, because the request line is written
// after the handler has finished and the standard ResponseWriter does not
// report either of these back.
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (rec *recorder) WriteHeader(status int) {
	if rec.status == 0 {
		rec.status = status
	}
	rec.ResponseWriter.WriteHeader(status)
}

func (rec *recorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	n, err := rec.ResponseWriter.Write(b)
	rec.bytes += int64(n)

	return n, err
}

// Unwrap keeps http.ResponseController working through this wrapper, so a
// handler that needs to flush or extend a deadline still can.
func (rec *recorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }

// Logging writes exactly one line per request, whatever answers it.
//
// The query string is never logged. A query carries exactly the kind of
// detail -- a search term, a phone number somebody pasted -- that should not
// outlive the request in a file somebody tails.
//
// It sits outside Panics so that a request answered by the panic recovery
// still gets its request line, with the 500 on it.
func Logging(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &recorder{ResponseWriter: w}

			next.ServeHTTP(rec, r)

			if rec.status == 0 {
				rec.status = http.StatusOK
			}

			log.InfoContext(r.Context(), "request",
				"id", RequestIDFrom(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.bytes,
				"ms", time.Since(start).Milliseconds(),
				"host", r.Host,
			)
		})
	}
}

// --- panics -----------------------------------------------------------------

// Panics turns a panic into one error line and a 500.
//
// Without it net/http prints an unstructured "panic serving" past a request
// line that was never written, which is the worst of both: no correlation id
// and no status. It sits inside Logging so the request line records the 500.
func Panics(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}

				log.ErrorContext(r.Context(), "panic",
					"id", RequestIDFrom(r.Context()),
					"method", r.Method,
					"path", r.URL.Path,
					"value", rec,
				)

				http.Error(w,
					"Something went wrong on our end. Try once more in a minute.",
					http.StatusInternalServerError)
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// --- response headers -------------------------------------------------------

// Policy is the set of response headers a surface answers with.
//
// It is a value rather than a constant because what a page may load and how
// long it may be cached is a product decision, and nothing in foundation/ may
// make one. Building the policy is the caller's job, one layer up, in
// app/sdk/page. An empty field is not set at all.
type Policy struct {
	ContentSecurityPolicy string
	ReferrerPolicy        string
	CacheControl          string
	FrameOptions          string
	StrictTransport       string
}

// PolicyFor chooses the policy for one request.
type PolicyFor func(*http.Request) Policy

// SecureHeaders sets the response headers before the handler can write a body.
//
// Every header is Set and never Add. Two enforced Content-Security-Policy
// headers are evaluated independently -- a request need only violate one to be
// blocked -- so a second, stricter policy arriving alongside ours would block
// everything while looking like our policy was ignored.
func SecureHeaders(policy PolicyFor) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := policy(r)
			head := w.Header()

			set := func(name, value string) {
				if value != "" {
					head.Set(name, value)
				}
			}

			set("Content-Security-Policy", p.ContentSecurityPolicy)
			set("Referrer-Policy", p.ReferrerPolicy)
			set("Cache-Control", p.CacheControl)
			set("X-Frame-Options", p.FrameOptions)
			set("Strict-Transport-Security", p.StrictTransport)
			head.Set("X-Content-Type-Options", "nosniff")

			next.ServeHTTP(w, r)
		})
	}
}
