package muxer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/translation/translationbus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/mail"
	"github.com/jroedel/reconcile/foundation/oauth"
	"github.com/jroedel/reconcile/foundation/sqldb"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// These are the ways a program reaches the translations (docs/translations.md):
// a key made on /account/keys, the API it opens, the same API as an MCP
// server, and OAuth for Claude on claude.ai. Through the whole site, so that
// every request passes the checks it would in production.

const (
	claudeAI = "https://claude.ai/oauth/test-client-metadata"
	callback = "https://claude.ai/api/mcp/auth_callback"

	// RFC 7636, appendix B.
	verifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
)

// clients is the metadata documents these tests know, in place of the
// network: Claude's, and nothing else, since a document on a host that is
// not trusted must never be read at all.
type clients struct{ t *testing.T }

func (c clients) Fetch(_ context.Context, id string) (oauth.Client, error) {
	if id != claudeAI {
		c.t.Errorf("a document was read for %s", id)

		return oauth.Client{}, oauth.ErrBadClient
	}

	return oauth.Client{ID: claudeAI, Name: "Claude", RedirectURIs: []string{callback}}, nil
}

type translatingSite struct {
	h     http.Handler
	sent  *mail.Recorder
	users *userbus.Business

	// admin is the site administrator, signed in, who translates every
	// language without being named (review_test.go names the others).
	admin *browser
}

func newTranslatingSite(t *testing.T) translatingSite {
	t.Helper()

	const secret = "a-setup-secret-that-is-long-enough-to-be-accepted"

	var s translatingSite

	s.h, s.sent = newSite(t, sqldb.Infrastructure, func(c *Config) {
		c.Bootstrap = secret
		c.OAuthClients = clients{t}
		s.users = c.Users
	})

	s.admin = newBrowser(t, s.h)
	wantRedirect(t, s.admin.post("/sign-in/first", url.Values{"email": {"admin@example.org"}, "secret": {secret}}), "/account?welcome=1")

	return s
}

var (
	keyShown  = regexp.MustCompile(`rcn_[A-Za-z0-9._-]+`)
	revokeOne = regexp.MustCompile(`action="(/account/keys/[^/"]+/revoke)"`)
)

// makeKey makes a translating key on the keys screen, as a translator does,
// and copies it off the page.
func makeKey(t *testing.T, b *browser, name string) string {
	t.Helper()

	return keyFor(t, b, name, "translate")
}

// keyFor makes a key for a purpose on the keys screen and copies it off the
// page.
func keyFor(t *testing.T, b *browser, name, purpose string) string {
	t.Helper()

	rec := b.post("/account/keys", url.Values{"name": {name}, "purpose": {purpose}})
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("making a key: %d %q\n%s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body)
	}

	key := keyShown.FindString(rec.Body.String())
	if key == "" {
		t.Fatalf("no key on the page:\n%s", rec.Body)
	}

	return key
}

// api sends a request as a program does: a key, JSON, and no browser.
func (s translatingSite) api(method, path, key, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}

	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}

	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, r)

	return rec
}

// Anybody may hold a key for a script that sends statements, but only a
// translator one that translates; the administrator makes one, sees it
// once, and can revoke it.
func TestOnlyATranslatorHasATranslatingKey(t *testing.T) {
	s := newTranslatingSite(t)

	stranger := signUp(t, s.h, s.sent, "stranger@example.org")

	wantBody(t, stranger.get("/account"), `href="/account/keys"`)

	page := stranger.get("/account/keys")
	wantBody(t, page, `value="upload"`)

	if strings.Contains(page.Body.String(), `value="translate"`) {
		t.Error("the keys screen offers translating to somebody who does not translate")
	}

	if rec := stranger.post("/account/keys", url.Values{"name": {"laptop"}, "purpose": {"translate"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a translating key for somebody who does not translate: %d", rec.Code)
	}

	if rec := stranger.post("/account/keys", url.Values{"name": {"laptop"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a key for nothing: %d", rec.Code)
	}

	theirs := keyFor(t, stranger, "Gmail script", "upload")
	if rec := s.api(http.MethodGet, "/api/v1/translations/pending?lang=es", theirs, ""); rec.Code != http.StatusForbidden {
		t.Errorf("an upload key on the translations: %d", rec.Code)
	}

	wantBody(t, s.admin.get("/account"), `href="/account/keys"`)

	key := makeKey(t, s.admin, "laptop")

	page = s.admin.get("/account/keys")
	wantBody(t, page, "laptop", "not used yet")

	if strings.Contains(page.Body.String(), key) {
		t.Error("the key is shown a second time")
	}

	if rec := s.admin.post("/account/keys", url.Values{"name": {"   "}, "purpose": {"translate"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a key with no name: %d", rec.Code)
	}

	// Revoked, it opens nothing.
	m := revokeOne.FindStringSubmatch(page.Body.String())
	if m == nil {
		t.Fatalf("no revoke button:\n%s", page.Body)
	}

	wantRedirect(t, s.admin.post(m[1], nil), "/account/keys?done=revoked")

	if rec := s.api(http.MethodGet, "/api/v1/translations/pending?lang=es", key, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("a revoked key: %d", rec.Code)
	}

	// Another person's key is not theirs to revoke: they are told there
	// is no key of theirs by that name now, which is true, and the key
	// still works.
	other := makeKey(t, s.admin, "desktop")
	id := strings.TrimPrefix(strings.SplitN(other, ".", 2)[0], userbus.APIKeyPrefix)

	wantRedirect(t, stranger.post("/account/keys/"+id+"/revoke", nil), "/account/keys?done=revoked")

	if rec := s.api(http.MethodGet, "/api/v1/translations/pending?lang=es", other, ""); rec.Code != http.StatusOK {
		t.Errorf("a key a stranger tried to revoke: %d", rec.Code)
	}
}

// A key reaches the translations, what it writes is on the pages at once,
// and it reaches nothing else; a cookie does not reach the API.
func TestAKeyReachesTheTranslationsAndNothingElse(t *testing.T) {
	s := newTranslatingSite(t)
	key := makeKey(t, s.admin, "laptop")

	// The index is open, and lists the three.
	idx := s.api(http.MethodGet, "/api/v1", "", "")
	for _, want := range []string{"list_pending_translations", "put_translations", "list_translations", base + "/account/keys"} {
		if !strings.Contains(idx.Body.String(), want) {
			t.Errorf("the index lacks %s:\n%s", want, idx.Body)
		}
	}

	// Without a key, and with only the administrator's cookie.
	if rec := s.api(http.MethodGet, "/api/v1/translations/pending?lang=es", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no key: %d", rec.Code)
	}

	if rec := s.admin.get("/api/v1/translations/pending?lang=es"); rec.Code != http.StatusUnauthorized {
		t.Errorf("a cookie on the API: %d", rec.Code)
	}

	// And the other way: a key does not open a page.
	r := httptest.NewRequest(http.MethodGet, "/account", nil)
	r.Header.Set("Authorization", "Bearer "+key)

	page := httptest.NewRecorder()
	s.h.ServeHTTP(page, r)

	if page.Code != http.StatusSeeOther || !strings.HasPrefix(page.Header().Get("Location"), "/sign-in") {
		t.Errorf("a key on a page: %d %q", page.Code, page.Header().Get("Location"))
	}

	// What is waiting.
	var pending struct {
		Lang    string `json:"lang"`
		Pending []struct {
			EN    string   `json:"en"`
			Pages []string `json:"pages"`
		} `json:"pending"`
		Remaining int `json:"remaining"`
	}

	rec := s.api(http.MethodGet, "/api/v1/translations/pending?lang=es&limit=200", key, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &pending); rec.Code != http.StatusOK || err != nil || pending.Lang != "es" || pending.Remaining < len(pending.Pending) || len(pending.Pending) == 0 {
		t.Fatalf("pending: %d %v\n%s", rec.Code, err, rec.Body)
	}

	for _, p := range pending.Pending {
		if p.EN == "Your account" && !slices.Contains(p.Pages, "account.html") {
			t.Errorf("Your account is on %v", p.Pages)
		}
	}

	// Two written, one refused for dropping its placeholder.
	rec = s.api(http.MethodPut, "/api/v1/translations", key, `{"lang": "es", "translations": [
		{"context": "", "en": "Your account", "text": "Tu cuenta"},
		{"context": "", "en": "You have {count} unused backup codes.", "text": "Te quedan {count} códigos de respaldo sin usar."},
		{"context": "", "en": "Make new backup codes", "text": "Hacer códigos nuevos {n}"}
	]}`)

	var put struct {
		Results []struct {
			EN, Outcome, Problem string
		} `json:"results"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &put); rec.Code != http.StatusOK || err != nil || len(put.Results) != 3 {
		t.Fatalf("put: %d %v\n%s", rec.Code, err, rec.Body)
	}

	if put.Results[0].Outcome != "created" || put.Results[1].Outcome != "created" || put.Results[2].Outcome != "refused" || !strings.Contains(put.Results[2].Problem, "placeholder") {
		t.Errorf("put: %+v", put.Results)
	}

	// On the page at once, in Spanish.
	wantRedirect(t, s.admin.post("/account/profile", url.Values{"name": {"Admin"}, "lang": {"es"}}), "/account?done=profile")
	wantBody(t, s.admin.get("/account"), "Tu cuenta")

	// As a draft, by Claude.
	rec = s.api(http.MethodGet, "/api/v1/translations?lang=es&status=draft", key, "")
	if !strings.Contains(rec.Body.String(), `"text": "Tu cuenta"`) || !strings.Contains(rec.Body.String(), `"origin": "claude"`) {
		t.Errorf("the drafts:\n%s", rec.Body)
	}

	// Somebody who does not translate cannot use a key even if one existed:
	// the screen will not make one, so it is made beneath it.
	signUp(t, s.h, s.sent, "stranger@example.org")

	addr, _ := types.ParseEmail("stranger@example.org")

	id, found, err := s.users.UserIDByEmail(t.Context(), addr)
	if err != nil || !found {
		t.Fatal(found, err)
	}

	_, theirs, err := s.users.CreateAPIKey(t.Context(), time.Now(), id, "sneaky", []userbus.Scope{userbus.Translate})
	if err != nil {
		t.Fatal(err)
	}

	if rec := s.api(http.MethodGet, "/api/v1/translations/pending?lang=es", theirs, ""); rec.Code != http.StatusForbidden {
		t.Errorf("a stranger's key: %d", rec.Code)
	}

	// Nothing else is under /api.
	for _, path := range []string{"/api/v1/accounts", "/api/v1/orgs", "/api/v2/translations", "/api/v1/export"} {
		if rec := s.api(http.MethodGet, path, key, ""); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "GET /api/v1 lists every endpoint") {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}

	// A write must be JSON.
	if rec := s.api(http.MethodPut, "/api/v1/translations", key, ""); rec.Code == http.StatusOK {
		t.Errorf("an empty PUT: %d", rec.Code)
	}

	// And a full batch of long strings fits through the body limit, which
	// is larger here than a form's for that reason.
	var batch []map[string]string
	for range translationbus.MaxBatch {
		batch = append(batch, map[string]string{"context": "", "en": strings.Repeat("A string nobody wrote. ", 10), "text": strings.Repeat("Una cadena que nadie escribió. ", 10)})
	}

	body, _ := json.Marshal(map[string]any{"lang": "es", "translations": batch})

	if rec := s.api(http.MethodPut, "/api/v1/translations", key, string(body)); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"outcome": "refused"`) {
		t.Errorf("a full batch of %d bytes: %d %.300s", len(body), rec.Code, rec.Body)
	}
}

// bearer adds the key to every request, as claude.ai does once signed in.
type bearer struct{ key string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)

	return http.DefaultTransport.RoundTrip(r)
}

func toolText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}

	return b.String()
}

// The same three as tools on /mcp, behind a real listener so that the SDK's
// own client talks to it as claude.ai's would.
func TestClaudeTranslatesThroughMCP(t *testing.T) {
	s := newTranslatingSite(t)
	key := makeKey(t, s.admin, "Claude")

	srv := httptest.NewServer(s.h)
	t.Cleanup(srv.Close)

	// Without a key, a 401 that says where to sign in.
	resp, err := http.Post(srv.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != `Bearer resource_metadata="`+base+`/.well-known/oauth-protected-resource/mcp"` {
		t.Fatalf("no key: %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}

	resp, err = http.Get(srv.URL + "/.well-known/oauth-protected-resource/mcp")
	if err != nil {
		t.Fatal(err)
	}

	var doc struct {
		Resource string   `json:"resource"`
		Servers  []string `json:"authorization_servers"`
	}
	json.NewDecoder(resp.Body).Decode(&doc)
	resp.Body.Close()

	if doc.Resource != base+"/mcp" || !slices.Equal(doc.Servers, []string{base}) {
		t.Errorf("the resource document: %+v", doc)
	}

	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)

	cs, err := c.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{key}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })

	if got := cs.InitializeResult().Instructions; !strings.Contains(got, "Latin American, with tú") {
		t.Errorf("the instructions:\n%s", got)
	}

	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}

	slices.Sort(names)

	if !slices.Equal(names, []string{"list_pending_translations", "list_translations", "put_translations"}) {
		t.Errorf("the tools: %v", names)
	}

	call := func(tool string, args map[string]any) *mcp.CallToolResult {
		t.Helper()

		res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}

		return res
	}

	if res := call("list_pending_translations", map[string]any{"lang": "pt"}); res.IsError || !strings.Contains(toolText(res), `"remaining"`) {
		t.Errorf("list_pending_translations: %s", toolText(res))
	}

	res := call("put_translations", map[string]any{"lang": "pt", "translations": []any{
		map[string]any{"context": "", "en": "Your account", "text": "Sua conta"},
	}})
	if res.IsError || !strings.Contains(toolText(res), `"outcome": "created"`) {
		t.Errorf("put_translations: %s", toolText(res))
	}

	if res := call("list_pending_translations", map[string]any{"lang": "fr"}); !res.IsError || !strings.Contains(toolText(res), "es for Spanish or pt for Portuguese") {
		t.Errorf("a language not offered: %v %s", res.IsError, toolText(res))
	}
}

// --- OAuth ------------------------------------------------------------------------

// asking is the request Claude sends a person's browser with.
func asking() url.Values {
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {claudeAI},
		"redirect_uri":          {callback},
		"state":                 {"the-programs-state"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {base + "/mcp"},
	}
}

func authorize(q url.Values) string { return "/oauth/authorize?" + q.Encode() }

// sentBack is where a redirect to the program goes, and its query.
func sentBack(t *testing.T, rec *httptest.ResponseRecorder) url.Values {
	t.Helper()

	to, err := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusSeeOther || err != nil || to.Scheme+"://"+to.Host+to.Path != callback {
		t.Fatalf("not sent back to the program: %d %q\n%s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}

	q := to.Query()
	if q.Get("state") != "the-programs-state" || q.Get("iss") != base {
		t.Errorf("the answer's state and iss: %v", q)
	}

	return q
}

// program sends a form as a program on a server does: no cookie, no
// Sec-Fetch-Site, no Origin.
func (s translatingSite) program(path string, form url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, r)

	return rec
}

func TestClaudeSignsInAsATranslatorAndItsKeyReachesTheAPI(t *testing.T) {
	s := newTranslatingSite(t)

	// What Claude reads first.
	var meta map[string]any

	rec := s.api(http.MethodGet, "/.well-known/oauth-authorization-server", "", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil || meta["issuer"] != base || meta["authorization_endpoint"] != base+"/oauth/authorize" || meta["client_id_metadata_document_supported"] != true {
		t.Fatalf("the metadata: %d %s", rec.Code, rec.Body)
	}

	// Signed out: sent to sign in, and back here.
	if rec := newBrowser(t, s.h).get(authorize(asking())); rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/sign-in?next=%2Foauth%2Fauthorize%3F") {
		t.Fatalf("signed out: %d %q", rec.Code, rec.Header().Get("Location"))
	}

	// Signed in: the page, which may send its form on to claude.ai.
	rec = s.admin.get(authorize(asking()))
	wantBody(t, rec, "Let Claude work as you?", "Claude (claude.ai)")

	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' https://claude.ai;") {
		t.Errorf("the page's policy: %s", csp)
	}

	form := asking()
	form.Set("answer", "allow")

	code := sentBack(t, s.admin.post("/oauth/authorize", form)).Get("code")
	if code == "" {
		t.Fatal("no code")
	}

	trade := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {claudeAI},
		"redirect_uri": {callback}, "code_verifier": {verifier}, "resource": {base + "/mcp"},
	}

	rec = s.program("/oauth/token", trade)

	var tok oauth.Token
	if err := json.Unmarshal(rec.Body.Bytes(), &tok); rec.Code != http.StatusOK || err != nil || tok.TokenType != "Bearer" || !strings.HasPrefix(tok.AccessToken, userbus.APIKeyPrefix) {
		t.Fatalf("the trade: %d %s", rec.Code, rec.Body)
	}

	if rec.Header().Get("Cache-Control") != "no-store" || tok.ExpiresIn < int64(userbus.APIKeyLife.Seconds())-60 {
		t.Errorf("Cache-Control %q, expires_in %d", rec.Header().Get("Cache-Control"), tok.ExpiresIn)
	}

	// An ordinary key: the API takes it, and the keys screen lists it by
	// the program's name.
	if rec := s.api(http.MethodGet, "/api/v1/translations/pending?lang=es", tok.AccessToken, ""); rec.Code != http.StatusOK {
		t.Errorf("the API with Claude's key: %d %s", rec.Code, rec.Body)
	}

	wantBody(t, s.admin.get("/account/keys"), "Claude (claude.ai)", "connected from Claude")

	// Once.
	rec = s.program("/oauth/token", trade)

	var refused oauth.TokenError
	if err := json.Unmarshal(rec.Body.Bytes(), &refused); rec.Code != http.StatusBadRequest || err != nil || refused.Code != oauth.InvalidGrant {
		t.Errorf("a second trade: %d %s", rec.Code, rec.Body)
	}

	// The wrong verifier, on a fresh code.
	code = sentBack(t, s.admin.post("/oauth/authorize", form)).Get("code")
	trade.Set("code", code)
	trade.Set("code_verifier", strings.Repeat("A", 43))

	if rec := s.program("/oauth/token", trade); rec.Code != http.StatusBadRequest {
		t.Errorf("the wrong verifier: %d %s", rec.Code, rec.Body)
	}
}

// Anybody signed in may connect claude.ai to keep their books, or, with
// the box ticked, only to read them; the page says which, and the key
// claude.ai gets is what the person chose. Translating is offered only to
// those who translate.
func TestAnybodyMayConnectClaudeToKeepTheirBooks(t *testing.T) {
	s := newTranslatingSite(t)
	stranger := signUp(t, s.h, s.sent, "stranger@example.org")

	rec := stranger.get(authorize(asking()))
	wantBody(t, rec, "Let Claude work as you?", "keep your books", "sort transactions, write sorting rules",
		"It cannot reconcile a month", `name="reading" value="only"`)

	if strings.Contains(rec.Body.String(), "Spanish and Portuguese") {
		t.Error("the page offers translating to somebody who does not translate")
	}

	wantBody(t, s.admin.get(authorize(asking())), "Spanish and Portuguese")

	connect := func(reading string) string {
		t.Helper()

		form := asking()
		form.Set("answer", "allow")

		if reading != "" {
			form.Set("reading", reading)
		}

		code := sentBack(t, stranger.post("/oauth/authorize", form)).Get("code")

		rec := s.program("/oauth/token", url.Values{
			"grant_type": {"authorization_code"}, "code": {code}, "client_id": {claudeAI},
			"redirect_uri": {callback}, "code_verifier": {verifier}, "resource": {base + "/mcp"},
		})

		var tok oauth.Token
		if err := json.Unmarshal(rec.Body.Bytes(), &tok); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("the trade: %d %s", rec.Code, rec.Body)
		}

		return tok.AccessToken
	}

	for reading, want := range map[string]string{"": "books:write", "only": "books:read"} {
		key := connect(reading)

		if scopes := field(s.get(t, "/api/v1/me", key), "scopes").([]any); len(scopes) != 1 || scopes[0] != want {
			t.Errorf("reading %q: the key's scopes are %v, want %s", reading, scopes, want)
		}

		if rec := s.api(http.MethodGet, "/api/v1/translations/pending?lang=es", key, ""); rec.Code != http.StatusForbidden {
			t.Errorf("reading %q: the translations with Claude's key: %d", reading, rec.Code)
		}

		code := http.StatusOK
		if want == "books:read" {
			code = http.StatusForbidden
		}

		if rec := s.api(http.MethodPost, "/api/v1/inbox/import", key, "{}"); rec.Code != code {
			t.Errorf("reading %q: importing from the inbox: %d, want %d", reading, rec.Code, code)
		}
	}
}

func TestSayingNoSendsClaudeAwayEmptyHanded(t *testing.T) {
	s := newTranslatingSite(t)

	form := asking()
	form.Set("answer", "deny")

	q := sentBack(t, s.admin.post("/oauth/authorize", form))
	if q.Get("error") != "access_denied" || q.Get("code") != "" {
		t.Errorf("saying no: %v", q)
	}
}

// A program or a redirect that cannot be trusted is answered on our own
// page; nothing is sent anywhere.
func TestAnUntrustedRequestStaysOnOurPage(t *testing.T) {
	s := newTranslatingSite(t)

	for name, change := range map[string][2]string{
		"another program":             {"client_id", "https://some-app.example.invalid/client"},
		"not https":                   {"client_id", "http://claude.ai/oauth/test-client-metadata"},
		"a look-alike":                {"client_id", "https://claude.ai.evil.example.invalid/client"},
		"a redirect it does not list": {"redirect_uri", "https://evil.example.invalid/cb"},
	} {
		q := asking()
		q.Set(change[0], change[1])

		rec := s.admin.get(authorize(q))
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
			t.Errorf("%s, asking: %d %q", name, rec.Code, rec.Header().Get("Location"))
		}

		// And the same through the form, whose hidden fields are the
		// browser's to change.
		q.Set("answer", "allow")

		rec = s.admin.post("/oauth/authorize", q)
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" {
			t.Errorf("%s, allowing: %d %q", name, rec.Code, rec.Header().Get("Location"))
		}
	}
}

// Everything else wrong with a request goes back to the program, which is
// how it finds out.
func TestARequestWithoutPKCEGoesBackWithAnError(t *testing.T) {
	s := newTranslatingSite(t)

	for _, drop := range []string{"code_challenge", "code_challenge_method", "response_type"} {
		q := asking()
		q.Del(drop)

		if got := sentBack(t, s.admin.get(authorize(q))); got.Get("error") == "" || got.Get("code") != "" {
			t.Errorf("without %s: %v", drop, got)
		}
	}
}

// The yes is a form post, so another site cannot make a signed-in browser
// send it: a code made that way would go to whoever started that sign-in at
// claude.ai, with the translator's account behind it.
func TestAnotherSiteCannotSayYes(t *testing.T) {
	s := newTranslatingSite(t)

	form := asking()
	form.Set("answer", "allow")

	r := httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "cross-site")

	if rec := s.admin.do(r); rec.Code != http.StatusForbidden || rec.Header().Get("Location") != "" {
		t.Errorf("a cross-site yes: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestTheTokenEndpointAnswersInTheRFCsWords(t *testing.T) {
	s := newTranslatingSite(t)

	for name, tc := range map[string]struct {
		form url.Values
		want string
	}{
		"a refresh":      {url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"x"}}, "unsupported_grant_type"},
		"no code":        {url.Values{"grant_type": {"authorization_code"}, "client_id": {claudeAI}, "redirect_uri": {callback}, "code_verifier": {verifier}}, "invalid_request"},
		"a made-up code": {url.Values{"grant_type": {"authorization_code"}, "code": {types.NewID().String() + ".AAAAAAAAAAAAAAAAAAAAAAAAAA"}, "client_id": {claudeAI}, "redirect_uri": {callback}, "code_verifier": {verifier}}, "invalid_grant"},
	} {
		rec := s.program("/oauth/token", tc.form)

		var got oauth.TokenError
		if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != http.StatusBadRequest || err != nil || got.Code != tc.want {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}
