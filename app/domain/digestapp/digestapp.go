// Package digestapp sends each person their weekly email
// (docs/accountant.md, 5), and is the page its link to stop it opens.
//
// The email is plain text, as every message the app sends is: counts and
// links, never an amount, a payee or an image, because every link opens a
// page that asks for sign-in, and what the email says is only that there
// is something there. digestbus says what is new; this writes it in the
// reader's language and sends it, at the day and hour they chose in their
// own time zone (userbus.DueWeekly).
//
// # Stopping it
//
// The link opens a page with one sentence and a button, and changes
// nothing until the button is pressed: the scanners some mail systems run
// over every link in a message open it too, and a link that stopped the
// email on opening would stop it for people who never asked. A mail
// program's own unsubscribe button posts to the same address (RFC 8058,
// the List-Unsubscribe-Post header), which is the one tap the scanners do
// not make. Neither needs signing in: the token in the link is the
// person's word, and it works for their latest email only.
package digestapp

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/jroedel/reconcile/business/domain/digest/digestbus"
	"github.com/jroedel/reconcile/business/domain/ledger/ledgerbus"
	"github.com/jroedel/reconcile/business/domain/user/userbus"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/mail"
)

//go:embed templates mail
var files embed.FS

// Templates is this app's page and its message, for page.NewRenderer.
var Templates fs.FS = files

// StopPath is where an email's link to stop it goes.
const StopPath = "/digest/stop"

// Renderer is the part of page.Renderer this app uses.
type Renderer interface {
	Render(w http.ResponseWriter, r *http.Request, status int, name string, data any)
	Mail(lang types.Lang, name string, data any) (subject, body string, err error)
}

// Users is whose email is due, taking it, and the token that stops it.
type Users interface {
	DueWeekly(ctx context.Context, now time.Time) ([]userbus.Due, error)
	ClaimWeek(ctx context.Context, now time.Time, userID types.ID, was time.Time) (bool, error)
	StopToken(ctx context.Context, userID types.ID) (string, error)
	StopWeekly(ctx context.Context, token string) (bool, error)
}

// Digest is what an email says (digestbus).
type Digest interface {
	Build(ctx context.Context, now time.Time, userID types.ID, since time.Time) (digestbus.Week, error)
	Sent(ctx context.Context, userID types.ID, w digestbus.Week) error
}

// Config is what this app needs.
type Config struct {
	Log    *slog.Logger
	Users  Users
	Digest Digest
	Render Renderer

	// Mail sends; nil sends nothing, and then no week is taken either, so
	// that a site with no relay does not move everybody's cursor past
	// emails nobody got.
	Mail mail.Sender

	// BaseURL is the public origin, for every link in the email.
	BaseURL string
}

// Routes mounts the stop page, outside the sign-in guard: the token is
// the credential.
func Routes(mux *http.ServeMux, cfg Config) {
	a := app{cfg: cfg}

	mux.HandleFunc("GET "+StopPath, a.stopPage)
	mux.HandleFunc("POST "+StopPath, a.stop)
}

type app struct {
	cfg Config
}

// stopView is the stop page: asking, with the token for the button; done;
// a token that is not, or no longer, anybody's; or a failure of ours.
type stopView struct {
	State string
	Token string
}

func (a app) stopPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	switch {
	case q.Get("done") == "1":
		a.cfg.Render.Render(w, r, http.StatusOK, "digest-stop", stopView{State: "done"})
	case q.Get("t") == "":
		a.cfg.Render.Render(w, r, http.StatusNotFound, "digest-stop", stopView{State: "unknown"})
	default:
		a.cfg.Render.Render(w, r, http.StatusOK, "digest-stop", stopView{State: "ask", Token: q.Get("t")})
	}
}

// stop takes the token from the form the page's button posts, or from the
// address, where a mail program's one-click button leaves it.
func (a app) stop(w http.ResponseWriter, r *http.Request) {
	ok, err := a.cfg.Users.StopWeekly(r.Context(), r.FormValue("t"))

	switch {
	case err != nil:
		a.cfg.Log.Error("a weekly email could not be stopped", "error", err)
		a.cfg.Render.Render(w, r, http.StatusInternalServerError, "digest-stop", stopView{State: "server", Token: r.FormValue("t")})
	case !ok:
		a.cfg.Render.Render(w, r, http.StatusNotFound, "digest-stop", stopView{State: "unknown"})
	default:
		http.Redirect(w, r, StopPath+"?done=1", http.StatusSeeOther)
	}
}

// Sender sends the weekly emails that are due.
type Sender struct {
	cfg Config
}

// NewSender constructs one.
func NewSender(cfg Config) *Sender {
	return &Sender{cfg: cfg}
}

// Run sends every email due at now, and keeps what each saw. One person's
// failure is logged and does not stop the others'.
func (s *Sender) Run(ctx context.Context, now time.Time) error {
	if s.cfg.Mail == nil {
		return nil
	}

	due, err := s.cfg.Users.DueWeekly(ctx, now)
	if err != nil {
		return err
	}

	for _, d := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err := s.send(ctx, now, d); err != nil {
			s.cfg.Log.Error("a weekly email could not be sent", "user_id", d.User.ID.String(), "error", err)
		}
	}

	return nil
}

// send takes a person's week, then says what is new, if anything. The
// week is taken first, so that two senders never both send it; and the
// cursor moves when there is nothing to say, too, so that the next email
// tells from now.
func (s *Sender) send(ctx context.Context, now time.Time, d userbus.Due) error {
	ok, err := s.cfg.Users.ClaimWeek(ctx, now, d.User.ID, d.Since)
	if err != nil || !ok {
		return err
	}

	week, err := s.cfg.Digest.Build(ctx, now, d.User.ID, d.Since)
	if err != nil {
		return err
	}

	if week.Empty() {
		return s.cfg.Digest.Sent(ctx, d.User.ID, week)
	}

	token, err := s.cfg.Users.StopToken(ctx, d.User.ID)
	if err != nil {
		return err
	}

	stop := s.cfg.BaseURL + StopPath + "?t=" + url.QueryEscape(token)

	subject, body, err := s.cfg.Render.Mail(d.User.Lang, "digest", s.letter(now, d, week, stop))
	if err != nil {
		return err
	}

	err = s.cfg.Mail.Send(ctx, mail.Message{
		To:      d.User.Email.String(),
		Subject: subject,
		Text:    body,
		Headers: [][2]string{
			{"List-Unsubscribe", "<" + stop + ">"},
			{"List-Unsubscribe-Post", "List-Unsubscribe=One-Click"},
		},
	})
	if err != nil {
		// What it saw is not kept, so the next email says this one's news
		// as new; its questions, read from the cursor, it does not.
		return err
	}

	return s.cfg.Digest.Sent(ctx, d.User.ID, week)
}

// letter is what the message template reads.
type letter struct {
	First    bool
	Date     string
	Orgs     []orgLetter
	Weekday  int
	Hour     string
	Zone     string
	Settings string
	Stop     string
}

type orgLetter struct {
	Name string

	NewlyNeeded, Needed int
	NeededLink          string

	NewlyReady int
	ReadyLink  string

	Asked, Answered []string
}

func (s *Sender) letter(now time.Time, d userbus.Due, week digestbus.Week, stop string) letter {
	zone := d.User.Zone
	if zone == "" {
		zone = "UTC"
	}

	l := letter{
		First:    d.Since.IsZero(),
		Date:     now.In(userbus.Location(d.User.Zone)).Format("2006-01-02"),
		Weekday:  int(d.Weekly.Weekday),
		Hour:     fmt.Sprintf("%02d:00", d.Weekly.Hour),
		Zone:     zone,
		Settings: s.cfg.BaseURL + "/account#weekly",
		Stop:     stop,
	}

	for _, o := range week.Orgs {
		lists := s.cfg.BaseURL + "/orgs/" + o.Org.ID.String() + "/accountant?context="

		ol := orgLetter{
			Name:        o.Org.Name,
			NewlyNeeded: o.NewlyNeeded,
			Needed:      o.Needed,
			NeededLink:  lists + string(ledgerbus.MoreContextNeeded),
			NewlyReady:  o.NewlyReady,
			ReadyLink:   lists + string(ledgerbus.ContextAdded),
		}

		for _, q := range o.Asked {
			ol.Asked = append(ol.Asked, s.cfg.BaseURL+"/questions/"+q.ID.String())
		}

		for _, q := range o.Answered {
			ol.Answered = append(ol.Answered, s.cfg.BaseURL+"/questions/"+q.ID.String())
		}

		l.Orgs = append(l.Orgs, ol)
	}

	return l
}
