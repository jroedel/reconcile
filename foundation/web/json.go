package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
)

// JSON is the content type the API speaks.
const JSON = "application/json"

// JSONOnly is FormEncodedOnly for the API: a write must say it is JSON. A
// form post to the API, or a page's fetch pretending to be one, is refused
// before anything reads it.
func JSONOnly() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if !safeMethod(r.Method) && r.ContentLength != 0 && (err != nil || kind != JSON) {
				WriteJSON(w, http.StatusUnsupportedMediaType, Problem("", "Send the body as JSON, with Content-Type: application/json."))

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// ProblemBody is what every refusal from the API looks like: the field to fix,
// when there is one, and a sentence saying what to do about it.
type ProblemBody struct {
	Error ProblemDetail `json:"error"`
}

// ProblemDetail is the inside of a ProblemBody.
type ProblemDetail struct {
	Field   string `json:"field,omitempty"`
	Problem string `json:"problem"`
}

// Problem builds one.
func Problem(field, problem string) ProblemBody {
	return ProblemBody{Error: ProblemDetail{Field: field, Problem: problem}}
}

// WriteJSON answers with v, indented: the reader is as likely to be a person
// at a terminal as a program, and the bytes are few.
//
// Without HTML escaping, so "<slug>" reads as itself rather than as
// "\u003cslug\u003e". The escaping is for JSON pasted into a page's script;
// these answers are served as application/json with nosniff, and never are.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")

	if err := enc.Encode(v); err != nil {
		// Only a value with a channel or a func in it fails to encode,
		// which is a bug here rather than anything a request did.
		http.Error(w, "the answer could not be written", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", JSON+"; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// ReadJSON decodes a request body into v, strictly: a field v does not have
// is an error, so a misspelt "ammount" is a refusal naming it rather than
// a transaction saved without its amount. The returned error is a
// sentence for the caller.
func ReadJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	err := dec.Decode(v)

	syntax, isSyntax := errors.AsType[*json.SyntaxError](err)
	typ, isType := errors.AsType[*json.UnmarshalTypeError](err)

	switch {
	case err == nil:
	case errors.Is(err, io.EOF):
		return errors.New("the body is empty. Send the fields as a JSON object")
	case isSyntax:
		return fmt.Errorf("the body is not valid JSON (at byte %d)", syntax.Offset)
	case isType:
		return fmt.Errorf("%s should be %s, not %s", typ.Field, typ.Type, typ.Value)
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return fmt.Errorf("%s is not a field this takes", strings.TrimPrefix(err.Error(), "json: unknown field "))
	default:
		if _, tooBig := errors.AsType[*http.MaxBytesError](err); tooBig {
			return errors.New("the body is too large")
		}

		return errors.New("the body could not be read as JSON")
	}

	if dec.More() {
		return errors.New("the body has more than one JSON value. Send one object")
	}

	return nil
}
