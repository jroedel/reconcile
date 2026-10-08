package filebus_test

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jroedel/reconcile/business/domain/file/filebus"
	"github.com/jroedel/reconcile/business/domain/file/stores/filedb"
	"github.com/jroedel/reconcile/business/domain/file/stores/filefs"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

func newBus(t *testing.T) (*filebus.Business, string) {
	t.Helper()

	dir := t.TempDir()

	db, err := sqldb.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	if err := filedb.Init(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	bytes, err := filefs.NewStore(filepath.Join(dir, "files"))
	if err != nil {
		t.Fatal(err)
	}

	return filebus.NewBusiness(slog.New(slog.NewTextHandler(io.Discard, nil)), filedb.NewStore(db), bytes), filepath.Join(dir, "files")
}

func TestSaveAndReadBack(t *testing.T) {
	b, dir := newBus(t)
	me := types.NewID()

	f, err := b.Save(t.Context(), time.Now(), me, `C:\Users\someone\Downloads\july "export".csv`, strings.NewReader("Date,Amount\n2026-07-01,1.00\n"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}

	if f.Name != "july export.csv" || f.ContentType != "text/plain; charset=utf-8" || f.Size != 28 || f.UploadedBy != me {
		t.Errorf("file = %+v", f)
	}

	got, err := b.ByID(t.Context(), f.ID)
	if err != nil || got.SHA256 != f.SHA256 {
		t.Fatalf("ByID = %+v, %v", got, err)
	}

	data, err := b.ReadAll(got)
	if err != nil || string(data) != "Date,Amount\n2026-07-01,1.00\n" {
		t.Errorf("ReadAll = %q, %v", data, err)
	}

	// The same content uploaded again is a second row and the same bytes.
	again, _ := b.Save(t.Context(), time.Now(), types.NewID(), "copy.csv", strings.NewReader("Date,Amount\n2026-07-01,1.00\n"), 1<<20)
	if again.ID == f.ID || again.SHA256 != f.SHA256 {
		t.Errorf("a second upload: %+v", again)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("%d files on disk, want one per content", len(entries))
	}

	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("the directory is %v; nobody else on the host should read it", info.Mode().Perm())
	}
}

func TestTooBigKeepsNothing(t *testing.T) {
	b, dir := newBus(t)

	if _, err := b.Save(t.Context(), time.Now(), types.NewID(), "big.csv", strings.NewReader(strings.Repeat("x", 101)), 100); !errors.Is(err, filebus.ErrTooBig) {
		t.Fatalf("err = %v", err)
	}

	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("%d files left behind", len(entries))
	}

	if _, err := b.Save(t.Context(), time.Now(), types.NewID(), "exact.csv", strings.NewReader(strings.Repeat("x", 100)), 100); err != nil {
		t.Errorf("exactly the limit: %v", err)
	}
}

func TestOpenRefusesANameThatIsNotAHash(t *testing.T) {
	store, err := filefs.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"../test.db", "", "abc"} {
		if _, err := store.Open(name); err == nil {
			t.Errorf("Open(%q) succeeded", name)
		}
	}
}
