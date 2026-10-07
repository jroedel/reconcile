package mid_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jroedel/reconcile/app/sdk/mid"
	"github.com/jroedel/reconcile/business/types"
)

// serve runs a request through Lang and reports what the handler saw.
func serve(t *testing.T, r *http.Request) (*httptest.ResponseRecorder, types.Lang) {
	t.Helper()

	var saw types.Lang
	h := mid.Lang()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		saw = mid.LangFrom(r.Context())
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	return rec, saw
}

func TestTheToggleIsRememberedAndTheAddressCleaned(t *testing.T) {
	rec, _ := serve(t, httptest.NewRequest(http.MethodGet, "/accounts/abc?lang=es&x=1", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want a redirect", rec.Code)
	}

	if got := rec.Header().Get("Location"); got != "/accounts/abc?x=1" {
		t.Errorf("sent back to %q, want the same address without lang", got)
	}

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "lang" || cookies[0].Value != "es" {
		t.Errorf("cookies = %v, want lang=es", cookies)
	}
}

func TestTheLanguageIsChosenInOrder(t *testing.T) {
	for name, tc := range map[string]struct {
		cookie string
		accept string
		want   types.Lang
	}{
		"nothing at all":                    {"", "", types.English},
		"a Spanish phone":                   {"", "es-MX,es;q=0.9,en;q=0.8", types.Spanish},
		"Latin American Spanish":            {"", "es-419", types.Spanish},
		"English ranked above Spanish":      {"", "en-US,en;q=0.9,es;q=0.5", types.English},
		"Spanish ranked above English":      {"", "en;q=0.3,es;q=0.8", types.Spanish},
		"a language the app does not speak": {"", "fr-FR", types.English},
		"the cookie beats the phone":        {"en", "es", types.English},
		"a cookie that makes no sense":      {"klingon", "es", types.Spanish},
		"a Brazilian phone":                 {"", "pt-BR,pt;q=0.9,en;q=0.5", types.Portuguese},
		"Portuguese ranked below Spanish":   {"", "pt;q=0.4,es;q=0.6", types.Spanish},
		"the cookie chose Portuguese":       {"pt", "en", types.Portuguese},
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.cookie != "" {
				r.AddCookie(&http.Cookie{Name: "lang", Value: tc.cookie})
			}
			if tc.accept != "" {
				r.Header.Set("Accept-Language", tc.accept)
			}

			if _, got := serve(t, r); got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// A form posted with ?lang= in its action must still reach its handler.
func TestAPostIsNeverRedirectedForItsLanguage(t *testing.T) {
	rec, _ := serve(t, httptest.NewRequest(http.MethodPost, "/sign-in?lang=es", nil))

	if rec.Code == http.StatusSeeOther {
		t.Error("a POST was redirected, which would drop its body")
	}
}

func TestSwitchURLKeepsTheRestOfTheQuery(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/accounts?x=1", nil)

	if got := mid.SwitchURL(r, types.Spanish); got != "/accounts?lang=es&x=1" {
		t.Errorf("SwitchURL = %q", got)
	}
}
