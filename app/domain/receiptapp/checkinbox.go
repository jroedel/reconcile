package receiptapp

import (
	"context"
	"errors"
	"mime"
	"net/http"

	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/receipt/receiptbus"
	"github.com/jroedel/reconcile/business/domain/tenancy/tenancybus"
	"github.com/jroedel/reconcile/business/types"
)

// A person's own check inbox (receiptbus, inbox.go; docs/phone.md, 7):
// the images of checks shared without choosing an account, each filed
// under its account afterwards, by the person here or by their Claude.
//
// Its pages are under /checks rather than /receipts/checks, which reads
// better and cannot be had: /receipts/{id}/files/{n} and a
// /receipts/checks/{id}/image would each match a path the other does,
// with neither more specific, and the muxer refuses the pair.

// CheckInboxPath is a person's own check inbox.
const CheckInboxPath = "/checks"

// checkInboxView is the page of a person's own check inbox.
type checkInboxView struct {
	Inbox receiptbus.CheckInbox

	// Accounts is where a check may be filed: every account the person
	// may add receipts to, checking accounts first. Names is every
	// account the person can see by its ID, for naming where a filed
	// check went.
	Accounts []tenancybus.Account
	Filed    map[types.ID]receiptbus.Receipt
	Names    map[types.ID]tenancybus.Account

	Done     string
	Problem  string
	Uploaded uploaded
}

// MaxFiledShown is how many checks filed already the page lists: enough
// for the last month's stack, so that a person can see where each went,
// and not every check of every year.
const MaxFiledShown = 60

func (a app) checkInboxRoutes(handle func(string, http.HandlerFunc)) {
	handle("GET "+CheckInboxPath, a.checkInbox)
	handle("GET "+CheckInboxPath+"/{id}/image", a.checkInboxImage)
	handle("GET "+CheckInboxPath+"/{id}/image/{size}", a.checkInboxImage)
	handle("POST "+CheckInboxPath+"/{id}/file", a.fileInboxCheck)
	handle("POST "+CheckInboxPath+"/{id}/remove", a.removeInboxCheck)
}

func (a app) checkInbox(w http.ResponseWriter, r *http.Request) {
	a.checkInboxPage(w, r, http.StatusOK, "")
}

func (a app) checkInboxPage(w http.ResponseWriter, r *http.Request, status int, problem string) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	q := r.URL.Query()
	view := checkInboxView{Done: q.Get("done"), Problem: problem, Uploaded: uploadedFrom(q)}

	var err error

	if view.Inbox, err = a.cfg.Receipts.CheckInbox(ctx, me.ID); err != nil {
		a.failed(w, r, err)

		return
	}

	if view.Accounts, view.Names, err = a.fileable(ctx, me.ID); err != nil {
		a.failed(w, r, err)

		return
	}

	if len(view.Inbox.Filed) > MaxFiledShown {
		view.Inbox.Filed = view.Inbox.Filed[:MaxFiledShown]
	}

	// Where each filed check went, among those the person may still see:
	// one filed into an account they have since lost is listed without.
	view.Filed = map[types.ID]receiptbus.Receipt{}

	for _, c := range view.Inbox.Filed {
		if v, err := a.cfg.Receipts.Receipt(ctx, me.ID, c.ReceiptID); err == nil {
			view.Filed[c.ID] = v.Receipt
		}
	}

	a.cfg.Render.Render(w, r, status, "receipts-checks", view)
}

// fileable is every account the actor may add receipts to, checking
// accounts first, since a check is drawn on one; and every account they
// see, by its ID.
func (a app) fileable(ctx context.Context, me types.ID) ([]tenancybus.Account, map[types.ID]tenancybus.Account, error) {
	ov, err := a.cfg.Tenancy.Overview(ctx, me)
	if err != nil {
		return nil, nil, err
	}

	all := append([]tenancybus.Account(nil), ov.Accounts...)
	for _, o := range ov.Orgs {
		all = append(all, o.Accounts...)
	}

	var (
		checking, other []tenancybus.Account
		names           = map[types.ID]tenancybus.Account{}
	)

	for _, acct := range all {
		names[acct.ID] = acct

		access, err := a.cfg.Tenancy.AccessTo(ctx, me, acct.Scope())
		if err != nil {
			return nil, nil, err
		}

		switch {
		case !access.Can(tenancybus.Receipts):
		case acct.Kind == tenancybus.Checking:
			checking = append(checking, acct)
		default:
			other = append(other, acct)
		}
	}

	return append(checking, other...), names, nil
}

// uploadToCheckInbox is the inbox's own form: each file a check of its
// own, waiting to be filed.
func (a app) uploadToCheckInbox(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	got, err := a.receive(r, me.ID)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	a.addToCheckInbox(w, r, me.ID, got)
}

// addToCheckInbox puts an upload's files into the actor's own check
// inbox, and goes there, which says what happened.
func (a app) addToCheckInbox(w http.ResponseWriter, r *http.Request, me types.ID, got received) {
	added := 0

	if len(got.files) > 0 {
		checks, err := a.cfg.Receipts.AddToCheckInbox(r.Context(), a.cfg.Now(), me, got.files)
		if err != nil {
			a.failed(w, r, err)

			return
		}

		added = len(checks)
	}

	http.Redirect(w, r, CheckInboxPath+"?"+got.report(added).Encode(), http.StatusSeeOther)
}

// checkInboxImage serves the image of a check in the actor's own inbox,
// or its smaller picture, as a receipt's file and picture are served.
func (a app) checkInboxImage(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	f, err := a.cfg.Receipts.InboxCheckFile(r.Context(), me.ID, id, 0)
	if err != nil {
		a.failed(w, r, err)

		return
	}

	if size := r.PathValue("size"); size != "" {
		rc, err := a.cfg.Files.Picture(r.Context(), f, filebus.Size(size))
		if errors.Is(err, filebus.ErrNoPicture) {
			http.Redirect(w, r, CheckInboxPath+"/"+id.String()+"/image", http.StatusSeeOther)

			return
		}

		if err != nil {
			a.failed(w, r, err)

			return
		}
		defer rc.Close()

		w.Header().Set("Content-Type", filebus.JPEG)
		w.Header().Set("Content-Disposition", "inline")
		w.Header().Set("Cache-Control", "private, max-age=86400")

		http.ServeContent(w, r, "", f.UploadedAt, rc)

		return
	}

	rc, err := a.cfg.Files.Open(f)
	if err != nil {
		a.failed(w, r, err)

		return
	}
	defer rc.Close()

	disposition := "attachment"
	if Shown(f.ContentType) {
		disposition = "inline"
	}

	w.Header().Set("Content-Type", f.ContentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": f.Name}))
	w.Header().Set("Cache-Control", "private, max-age=3600")

	http.ServeContent(w, r, "", f.UploadedAt, rc)
}

// fileInboxCheck files a check from the actor's inbox under the account
// chosen for it (receiptbus.FileInboxCheck).
func (a app) fileInboxCheck(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !form(w, r) {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	account, err := types.ParseID(r.PostForm.Get("account"))
	if err != nil {
		a.checkInboxPage(w, r, http.StatusUnprocessableEntity, "account")

		return
	}

	rc, err := a.cfg.Receipts.FileInboxCheck(r.Context(), a.cfg.Now(), me.ID, id, account, r.PostForm.Get("number"))

	invalid, isInvalid := errors.AsType[receiptbus.Invalid](err)

	switch {
	case isInvalid:
		a.checkInboxPage(w, r, http.StatusUnprocessableEntity, invalid.Field)

		return
	case errors.Is(err, receiptbus.ErrFiled):
		a.checkInboxPage(w, r, http.StatusConflict, "filed")

		return
	case err != nil:
		a.failed(w, r, err)

		return
	}

	done := "filed-waiting"
	if !rc.Waiting() {
		done = "filed-attached"
	}

	http.Redirect(w, r, CheckInboxPath+"?done="+done, http.StatusSeeOther)
}

// removeInboxCheck takes a waiting check out of the actor's inbox, or
// brings it back.
func (a app) removeInboxCheck(w http.ResponseWriter, r *http.Request) {
	me, ok := actor(w, r)
	if !ok || !form(w, r) {
		return
	}

	id, ok := a.pathID(w, r)
	if !ok {
		return
	}

	removed := r.PostForm.Get("removed") == "1"

	_, err := a.cfg.Receipts.SetInboxCheckRemoved(r.Context(), a.cfg.Now(), me.ID, id, removed)

	switch {
	case errors.Is(err, receiptbus.ErrFiled):
		a.checkInboxPage(w, r, http.StatusConflict, "filed")

		return
	case err != nil:
		a.failed(w, r, err)

		return
	}

	done := "check-restored"
	if removed {
		done = "check-removed"
	}

	http.Redirect(w, r, CheckInboxPath+"?done="+done, http.StatusSeeOther)
}
