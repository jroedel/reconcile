package web

import (
	"net/http"
	"time"
)

// Deadline gives a route longer than the server's 30 seconds to read its
// request and write its answer: an upload from a phone on one bar, where 8 MB
// at a megabit a second is over a minute on its own.
//
// Per route, through http.ResponseController, as serve.go asks, and only on
// the routes that take files. A failure to set it -- a ResponseWriter that
// cannot -- leaves the server's deadline, which is the safe direction.
func Deadline(d time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(time.Now().Add(d))
			_ = rc.SetWriteDeadline(time.Now().Add(d + time.Minute))

			next.ServeHTTP(w, r)
		})
	}
}
