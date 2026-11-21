package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A request is in flight while it is answered, and not before or after;
// each change is reported once.
func TestInFlight(t *testing.T) {
	f := NewInFlight()

	var during []Flight

	h := Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		during = f.Now()
	}), RequestID(), f.Track())

	if f.Changed() || len(f.Now()) != 0 {
		t.Fatal("a new one should be empty and unchanged")
	}

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/receipts/abc/files/0/large?secret=1", nil))

	if len(during) != 1 || during[0].Method != "GET" || during[0].Path != "/receipts/abc/files/0/large" || during[0].ID == "" {
		t.Errorf("while answering: %+v", during)
	}

	if len(f.Now()) != 0 {
		t.Errorf("after answering: %+v", f.Now())
	}

	if !f.Changed() || f.Changed() {
		t.Error("a change should be reported once")
	}

	var none *InFlight
	if none.Track() != nil {
		t.Error("a nil InFlight should give no middleware")
	}
}
