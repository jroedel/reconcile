package muxer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jroedel/reconcile/foundation/sqldb"
)

// The app Chrome installs: a manifest anybody may read, with its icons at
// the sizes it says and a share target for images and PDFs; the worker
// that catches a share, allowed the share target's scope; and both linked
// from the pages of somebody signed in, under a policy that lets them in.
func TestTheAppIsInstallable(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)
	nobody := newBrowser(t, h)

	rec := nobody.get("/receipts/app.webmanifest")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/manifest+json" {
		t.Fatalf("the manifest: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}

	var m struct {
		Icons       []struct{ Src, Sizes, Purpose string }
		ShareTarget struct {
			Action, Method, Enctype string
			Params                  struct {
				Files []struct {
					Name   string
					Accept []string
				}
			}
		} `json:"share_target"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("the manifest is not JSON: %v", err)
	}

	st := m.ShareTarget
	if st.Action != "/receipts/shared" || st.Method != "POST" || st.Enctype != "multipart/form-data" ||
		len(st.Params.Files) != 1 || st.Params.Files[0].Name != "files" || strings.Join(st.Params.Files[0].Accept, " ") != "image/* application/pdf" {
		t.Errorf("the share target: %+v", st)
	}

	sizes := map[string]bool{}

	for _, ic := range m.Icons {
		img := nobody.get(ic.Src)
		if img.Code != http.StatusOK || img.Header().Get("Content-Type") != "image/png" {
			t.Errorf("%s: %d %q", ic.Src, img.Code, img.Header().Get("Content-Type"))

			continue
		}

		cfg, err := png.DecodeConfig(bytes.NewReader(img.Body.Bytes()))
		if err != nil || ic.Sizes != fmt.Sprintf("%dx%d", cfg.Width, cfg.Height) {
			t.Errorf("%s says %s and is %dx%d: %v", ic.Src, ic.Sizes, cfg.Width, cfg.Height, err)
		}

		sizes[ic.Sizes+" "+ic.Purpose] = true
	}

	for _, want := range []string{"192x192 any", "512x512 any", "512x512 maskable"} {
		if !sizes[want] {
			t.Errorf("no %s icon: %v", want, sizes)
		}
	}

	worker := nobody.get("/receipts/static/share-worker.mjs")
	if worker.Code != http.StatusOK || worker.Header().Get("Service-Worker-Allowed") != "/receipts/shared" ||
		!strings.HasPrefix(worker.Header().Get("Content-Type"), "text/javascript") {
		t.Errorf("the worker: %d %v", worker.Code, worker.Header())
	}

	if rec := nobody.get("/receipts/static/nothing.mjs"); rec.Code != http.StatusNotFound {
		t.Errorf("a script that is not there: %d", rec.Code)
	}

	page := e.owner.get("/")
	wantBody(t, page, `<link rel="manifest" href="/receipts/app.webmanifest">`, `<script type="module" src="/receipts/static/share.mjs">`)

	if csp := page.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "manifest-src 'self'") {
		t.Errorf("the policy keeps the manifest out: %s", csp)
	}

	if body := nobody.get("/sign-in").Body.String(); strings.Contains(body, "app.webmanifest") {
		t.Error("somebody not signed in is offered the app")
	}
}

// The share page offers every inbox the person may add to -- a checking
// account's check images, its receipts, a project's -- and nothing of
// anybody else's. Its form adds the files where it says: check images
// matched or waiting, receipts waiting. Where is asked first, so somebody
// who may not add there is refused before a file is kept.
func TestSharedFilesGoWhereTheyAreSent(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	account := strings.TrimPrefix(e.account, "/accounts/")
	project := strings.TrimPrefix(e.project, "/projects/")

	wantBody(t, e.owner.get("/receipts/share?shared=2"),
		"Add what you shared", "2 files from your phone, ready to add",
		`value="checks:`+account+`"`, "Check images · Parish checking",
		`value="account:`+account+`"`, "Receipts · Parish checking",
		`value="project:`+project+`"`, "Receipts · World Youth Day (project)",
		"Receipts · My card")

	if strings.Contains(e.owner.get("/receipts/share").Body.String(), "Check images · My card") {
		t.Error("a card is offered check images")
	}

	rec := e.owner.receipts("/receipts/share", url.Values{"to": {"checks:" + account}},
		[2]string{"Screenshot_20260712-101500.png", photoOf("one")}, [2]string{"Screenshot_20260712-101530.png", photoOf("two")})
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, e.account+"/receipts?") || !strings.Contains(loc, "done=checks") {
		t.Fatalf("checks shared: %d to %q", rec.Code, loc)
	}

	wantBody(t, e.owner.get(rec.Header().Get("Location")), "2 check images are waiting below")

	rec = e.owner.receipts("/receipts/share", url.Values{"to": {"project:" + project}}, [2]string{"hostel.jpg", photoOf("hostel")})
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, e.project+"/receipts?") {
		t.Fatalf("a receipt shared: %d to %q", rec.Code, loc)
	}

	wantBody(t, e.owner.get(rec.Header().Get("Location")), "1 receipts added", "hostel.jpg")

	// Somebody else sees none of these, and cannot send to them.
	stranger := signUp(t, h, sent, "stranger@example.org")

	body := stranger.get("/receipts/share").Body.String()
	if strings.Contains(body, account) || strings.Contains(body, project) {
		t.Error("a stranger is offered the owner's inboxes")
	}

	if rec := stranger.receipts("/receipts/share", url.Values{"to": {"checks:" + account}}, [2]string{"1176.jpg", photoOf("x")}); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger sharing to the owner's account: %d", rec.Code)
	}

	wantRedirect(t, e.owner.post(e.account+"/people", url.Values{"email": {"viewer@example.org"}, "role": {"viewer"}}), e.account+"?done=granted")
	viewer := signUp(t, h, sent, "viewer@example.org")

	if strings.Contains(viewer.get("/receipts/share").Body.String(), account) {
		t.Error("a viewer is offered an account they may not add to")
	}

	if rec := viewer.receipts("/receipts/share", url.Values{"to": {"account:" + account}}, [2]string{"a.jpg", photoOf("x")}); rec.Code != http.StatusForbidden {
		t.Errorf("a viewer sharing: %d", rec.Code)
	}

	// A form that does not say where first, or says somewhere that is
	// not one, keeps nothing.
	if rec := e.owner.receipts("/receipts/share", nil, [2]string{"a.jpg", photoOf("x")}); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/receipts/share" {
		t.Errorf("no where: %d to %q", rec.Code, rec.Header().Get("Location"))
	}

	if rec := e.owner.receipts("/receipts/share", url.Values{"to": {"ledger:" + account}}, [2]string{"a.jpg", photoOf("x")}); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown where: %d", rec.Code)
	}
}

// A share the phone's worker missed reaches the server, keeps nothing,
// and is asked for again; one with too many files is told to come in
// smaller batches; one the phone lost says so.
func TestAShareTheWorkerMissedIsAskedForAgain(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	rec := e.owner.receipts("/receipts/shared", nil, [2]string{"PXL_1.jpg", photoOf("x")})
	wantRedirect(t, rec, "/receipts/share?shared=again")
	wantBody(t, e.owner.get("/receipts/share?shared=again"), "did not reach this page")

	wantBody(t, e.owner.get("/receipts/share?shared=70"), "You shared 70 files, and at most 60 are added at once")
	wantBody(t, e.owner.get("/receipts/share?shared=lost"), "could not keep what you shared")

	if body := e.owner.get(e.account + "/receipts").Body.String(); strings.Contains(body, "PXL_1.jpg") {
		t.Error("a missed share was kept")
	}
}

// The share page's script sends a file a request, where first. Each answer
// says whether it was kept, and a check's image whether it was attached;
// the same bytes again are there already, and nothing is added. A file
// that is no receipt, a place the person may not add to, and one that is
// not said, are refused with a code the page says in its own words.
func TestASharedFileGoesOneARequest(t *testing.T) {
	h, sent := newSite(t, sqldb.Infrastructure, nil)
	e := newEstate(t, h, sent)

	account := strings.TrimPrefix(e.account, "/accounts/")
	project := strings.TrimPrefix(e.project, "/projects/")

	preview := uploaded(t, e.owner, e.account, "july.csv", "Date,Description,Amount,Balance,Check Number\n"+
		"2026-07-01,OPENING DEPOSIT,1000.00,1000.00,\n"+
		"2026-07-02,CHECK,-120.00,880.00,1176\n")
	form := columns("import")
	form.Set("check", "Check Number")

	if rec := e.owner.post(preview, form); rec.Code != http.StatusSeeOther {
		t.Fatalf("import: %d", rec.Code)
	}

	one := func(b *browser, to string, file [2]string) (int, map[string]any) {
		t.Helper()

		fields := url.Values{}
		if to != "" {
			fields.Set("to", to)
		}

		rec := b.receipts("/receipts/share/one", fields, file)

		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("not JSON: %d %s", rec.Code, rec.Body)
		}

		return rec.Code, out
	}

	for name, c := range map[string]struct {
		to       string
		file     [2]string
		status   int
		outcome  string
		attached bool
	}{
		"a screenshot":       {"checks:" + account, [2]string{"Screenshot_1.png", photoOf("one")}, http.StatusOK, "kept", false},
		"a named check":      {"checks:" + account, [2]string{"1176.jpg", photoOf("1176")}, http.StatusOK, "kept", true},
		"a project receipt":  {"project:" + project, [2]string{"hostel.jpg", photoOf("hostel")}, http.StatusOK, "kept", false},
		"an account receipt": {"account:" + account, [2]string{"bank.jpg", photoOf("bank")}, http.StatusOK, "kept", false},
	} {
		code, out := one(e.owner, c.to, c.file)
		if code != c.status || out["outcome"] != c.outcome || out["attached"] != c.attached {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}

	// Sent again: there already, whatever it is called now.
	if code, out := one(e.owner, "checks:"+account, [2]string{"Screenshot_1 (1).png", photoOf("one")}); code != http.StatusOK || out["outcome"] != "already" {
		t.Errorf("sent again: %d %v", code, out)
	}

	wantBody(t, e.owner.get(e.account+"/receipts"), "Check 1176", "A check", "bank.jpg")

	if body := e.owner.get(e.account + "/receipts").Body.String(); strings.Count(body, "its number not yet said") != 1 {
		t.Error("the screenshot sent twice is in the inbox twice")
	}

	wantBody(t, e.owner.get(e.account+"/receipts?done=checks&n=0&waiting=1&already=1"), "1 were in this inbox already")

	stranger := signUp(t, h, sent, "stranger@example.org")
	wantRedirect(t, e.owner.post(e.account+"/people", url.Values{"email": {"viewer@example.org"}, "role": {"viewer"}}), e.account+"?done=granted")
	viewer := signUp(t, h, sent, "viewer@example.org")

	for name, c := range map[string]struct {
		b      *browser
		to     string
		file   [2]string
		status int
		field  string
		code   string
	}{
		"not a receipt":       {e.owner, "checks:" + account, [2]string{"notes.txt", "plain words"}, http.StatusUnprocessableEntity, "files", "kind"},
		"nowhere said":        {e.owner, "", [2]string{"a.jpg", photoOf("a")}, http.StatusNotFound, "to", "to"},
		"somewhere not a one": {e.owner, "ledger:" + account, [2]string{"a.jpg", photoOf("a")}, http.StatusNotFound, "to", "to"},
		"a stranger":          {stranger, "checks:" + account, [2]string{"a.jpg", photoOf("a")}, http.StatusNotFound, "to", "to"},
		"a viewer":            {viewer, "account:" + account, [2]string{"a.jpg", photoOf("a")}, http.StatusForbidden, "to", "to"},
	} {
		code, out := one(c.b, c.to, c.file)
		if code != c.status || field(out, "error", "field") != c.field || field(out, "error", "problem") != c.code {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
}
