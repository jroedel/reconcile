package userbus_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/user/userbus"
)

func TestEmailChange(t *testing.T) {
	b := newBus(t)
	u := signUp(t, b, "old@example.org")
	to := addr(t, "new@example.org")

	req, err := b.RequestEmailChange(t.Context(), start, u.ID, to)
	if err != nil || req.Code == "" || req.Pending == "" {
		t.Fatalf("RequestEmailChange = %+v, %v", req, err)
	}

	// Nothing has changed yet.
	if still, _ := b.ByID(t.Context(), u.ID); still.Email != u.Email {
		t.Fatal("the address changed before the new one was proved")
	}

	if pending, ok, _ := b.PendingEmailChange(t.Context(), start, u.ID); !ok || pending != to {
		t.Errorf("PendingEmailChange = %v, %v", pending, ok)
	}

	moved, old, err := b.ConfirmEmailChange(t.Context(), start.Add(time.Minute), u.ID, req.Pending, req.Code)
	if err != nil || moved.Email != to || old != u.Email || moved.ID != u.ID {
		t.Fatalf("ConfirmEmailChange = %+v, %v, %v", moved, old, err)
	}

	// The new address signs in as the same user, and the old one is a
	// stranger now.
	if again := signUp(t, b, "new@example.org"); again.ID != u.ID {
		t.Error("the new address signed in as somebody else")
	}

	if fresh := signUp(t, b, "old@example.org"); fresh.ID == u.ID {
		t.Error("the old address still signs in as the user who left it")
	}

	if _, _, err := b.ConfirmEmailChange(t.Context(), start, u.ID, req.Pending, req.Code); !errors.Is(err, userbus.ErrDenied) {
		t.Errorf("a change confirmed twice: %v", err)
	}
}

func TestEmailChangeRefusals(t *testing.T) {
	b := newBus(t)
	u := signUp(t, b, "a@example.org")
	other := signUp(t, b, "b@example.org")

	if _, err := b.RequestEmailChange(t.Context(), start, u.ID, u.Email); !errors.Is(err, userbus.ErrSameAddress) {
		t.Errorf("a change to the same address: %v", err)
	}

	if _, err := b.RequestEmailChange(t.Context(), start, u.ID, other.Email); !errors.Is(err, userbus.ErrAddressTaken) {
		t.Errorf("a change to a taken address: %v", err)
	}

	req, _ := b.RequestEmailChange(t.Context(), start, u.ID, addr(t, "c@example.org"))

	t.Run("somebody else's session", func(t *testing.T) {
		if _, _, err := b.ConfirmEmailChange(t.Context(), start, other.ID, req.Pending, req.Code); !errors.Is(err, userbus.ErrDenied) {
			t.Fatalf("another user confirmed it: %v", err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		if _, _, err := b.ConfirmEmailChange(t.Context(), start.Add(16*time.Minute), u.ID, req.Pending, req.Code); !errors.Is(err, userbus.ErrDenied) {
			t.Fatalf("an expired change: %v", err)
		}
	})

	t.Run("out of tries", func(t *testing.T) {
		req, _ := b.RequestEmailChange(t.Context(), start, u.ID, addr(t, "d@example.org"))

		for range 5 {
			b.ConfirmEmailChange(t.Context(), start, u.ID, req.Pending, wrong(req.Code))
		}

		if _, _, err := b.ConfirmEmailChange(t.Context(), start, u.ID, req.Pending, req.Code); !errors.Is(err, userbus.ErrDenied) {
			t.Fatalf("the right code after five wrong ones: %v", err)
		}
	})

	t.Run("superseded", func(t *testing.T) {
		first, _ := b.RequestEmailChange(t.Context(), start, u.ID, addr(t, "e@example.org"))
		b.RequestEmailChange(t.Context(), start, u.ID, addr(t, "f@example.org"))

		if _, _, err := b.ConfirmEmailChange(t.Context(), start, u.ID, first.Pending, first.Code); !errors.Is(err, userbus.ErrDenied) {
			t.Fatalf("a change still worked after a newer one was asked for: %v", err)
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		req, _ := b.RequestEmailChange(t.Context(), start, u.ID, addr(t, "g@example.org"))

		if err := b.CancelEmailChange(t.Context(), start, u.ID); err != nil {
			t.Fatal(err)
		}

		if _, ok, _ := b.PendingEmailChange(t.Context(), start, u.ID); ok {
			t.Error("a cancelled change is still pending")
		}

		if _, _, err := b.ConfirmEmailChange(t.Context(), start, u.ID, req.Pending, req.Code); !errors.Is(err, userbus.ErrDenied) {
			t.Fatalf("a cancelled change was confirmed: %v", err)
		}
	})
}

// Somebody signs up with the address in the fifteen minutes before the change
// is confirmed: said plainly, and nobody is moved.
func TestEmailChangeToAnAddressTakenSince(t *testing.T) {
	b := newBus(t)
	u := signUp(t, b, "a@example.org")

	req, _ := b.RequestEmailChange(t.Context(), start, u.ID, addr(t, "contested@example.org"))
	signUp(t, b, "contested@example.org")

	if _, _, err := b.ConfirmEmailChange(t.Context(), start, u.ID, req.Pending, req.Code); !errors.Is(err, userbus.ErrAddressTaken) {
		t.Fatalf("ConfirmEmailChange = %v, want ErrAddressTaken", err)
	}

	if still, _ := b.ByID(t.Context(), u.ID); still.Email != u.Email {
		t.Error("the user was moved anyway")
	}
}
