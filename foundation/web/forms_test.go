package web_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/foundation/web"
)

const site = "https://reconcile.example.invalid"

func passes(t *testing.T, mw web.Middleware, r *http.Request) bool {
	t.Helper()

	reached := false
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	h.ServeHTTP(httptest.NewRecorder(), r)

	return reached
}

func post(headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/sign-in", strings.NewReader("email=a%40example.org"))

	// As konsoleH's proxy delivers it: the Host is the loopback, never the
	// public name. The first version of this check would refuse its own forms
	// here.
	r.Host = "127.0.0.1:8451"

	for k, v := range headers {
		r.Header.Set(k, v)
	}

	return r
}

func TestAWriteFromAnotherSiteIsRefused(t *testing.T) {
	mw := web.SameOriginOnly(site + "/")

	for name, tc := range map[string]struct {
		headers map[string]string
		want    bool
	}{
		"our own page":                      {map[string]string{"Sec-Fetch-Site": "same-origin"}, true},
		"a typed address":                   {map[string]string{"Sec-Fetch-Site": "none"}, true},
		"another site":                      {map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		"a sibling subdomain":               {map[string]string{"Sec-Fetch-Site": "same-site"}, false},
		"an old browser, from our page":     {map[string]string{"Origin": site}, true},
		"an old browser, from another site": {map[string]string{"Origin": "https://evil.example"}, false},
		"an old browser, over plain http":   {map[string]string{"Origin": "http://reconcile.example.invalid"}, false},
		"a null Origin from a native form":  {map[string]string{"Origin": "null"}, true},
		"neither header, as curl sends":     {nil, true},
	} {
		if got := passes(t, mw, post(tc.headers)); got != tc.want {
			t.Errorf("%s: passed = %v, want %v", name, got, tc.want)
		}
	}

	// A read is never checked.
	get := httptest.NewRequest(http.MethodGet, "/", nil)
	get.Header.Set("Sec-Fetch-Site", "cross-site")
	if !passes(t, mw, get) {
		t.Error("a cross-site GET was refused")
	}
}

func TestABodyOverTheLimitIsCutOff(t *testing.T) {
	var readErr error
	h := web.MaxBody(8)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	}))

	h.ServeHTTP(httptest.NewRecorder(), post(nil))

	if readErr == nil {
		t.Error("a body over the limit was read in full")
	}
}

func TestOnlyAFormPostIsAccepted(t *testing.T) {
	mw := web.FormEncodedOnly()

	for kind, want := range map[string]bool{
		"application/x-www-form-urlencoded":                true,
		"application/x-www-form-urlencoded; charset=UTF-8": true,
		"multipart/form-data; boundary=x":                  false,
		"application/json":                                 false,
		"application/x-www-form-urlencoded-and-more":       false,
		"": false,
	} {
		if got := passes(t, mw, post(map[string]string{"Content-Type": kind})); got != want {
			t.Errorf("%q: passed = %v, want %v", kind, got, want)
		}
	}
}

// The upload route's check is the mirror image: a file form and nothing else.
func TestOnlyAFileFormIsAcceptedWhereAFileIsExpected(t *testing.T) {
	mw := web.MultipartOnly()

	for kind, want := range map[string]bool{
		"multipart/form-data; boundary=x":   true,
		"application/x-www-form-urlencoded": false,
		"multipart/mixed; boundary=x":       false,
		"multipart/form-data-ish":           false,
		"":                                  false,
	} {
		if got := passes(t, mw, post(map[string]string{"Content-Type": kind})); got != want {
			t.Errorf("%q: passed = %v, want %v", kind, got, want)
		}
	}

	// A page read has no body to be the wrong shape.
	get := httptest.NewRequest(http.MethodGet, "/", nil)
	if !passes(t, mw, get) {
		t.Error("a GET was refused")
	}
}

func TestTheAPIOnlyTakesJSON(t *testing.T) {
	mw := web.JSONOnly()

	for kind, want := range map[string]bool{
		"application/json":                  true,
		"application/json; charset=utf-8":   true,
		"application/x-www-form-urlencoded": false,
		"text/plain":                        false,
		"":                                  false,
	} {
		r := post(map[string]string{"Content-Type": kind})
		r.Body = io.NopCloser(strings.NewReader("{}"))
		r.ContentLength = 2

		if got := passes(t, mw, r); got != want {
			t.Errorf("%q: passed = %v, want %v", kind, got, want)
		}
	}
}

func TestAnAnswerReadsAsWritten(t *testing.T) {
	w := httptest.NewRecorder()
	web.WriteJSON(w, http.StatusTeapot, map[string]string{"path": "/accounts/<id>"})

	if w.Code != http.StatusTeapot || !strings.Contains(w.Body.String(), `"/accounts/<id>"`) || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Errorf("%d %q %q", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
}
