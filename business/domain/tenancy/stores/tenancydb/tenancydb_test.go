package tenancydb_test

import (
	"path/filepath"
	"testing"

	"github.com/jroedel/reconcile/business/domain/tenancy/stores/tenancydb"
	"github.com/jroedel/reconcile/business/domain/user/stores/userdb"
	"github.com/jroedel/reconcile/business/types"
	"github.com/jroedel/reconcile/foundation/sqldb"
)

// orgsBeforeBudgets is the orgs table as the release before budget years
// made it: written out, not derived from Init, because the point is a
// database made by a different binary.
const orgsBeforeBudgets = `
CREATE TABLE IF NOT EXISTS orgs (
    id          TEXT    PRIMARY KEY,
    name        TEXT    NOT NULL,
    created_by  TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    archived_at INTEGER
) STRICT;
`

// An organization made before budget years has one that starts in
// January, however often Init runs.
func TestInitGivesOldOrganizationsAJanuaryYear(t *testing.T) {
	db, err := sqldb.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	ctx := t.Context()

	if err := userdb.Init(ctx, db); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, orgsBeforeBudgets); err != nil {
		t.Fatal(err)
	}

	id := types.NewID()
	if _, err := db.ExecContext(ctx, `INSERT INTO orgs (id, name, created_by, created_at) VALUES (?, 'St. Joseph Parish', ?, 1)`,
		id.String(), types.NewID().String()); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := tenancydb.Init(ctx, db); err != nil {
			t.Fatal(err)
		}
	}

	o, err := tenancydb.NewStore(db).OrgByID(ctx, id)
	if err != nil || o.FiscalStart != 1 || o.Name != "St. Joseph Parish" {
		t.Errorf("the organization: %+v %v", o, err)
	}

	if err := sqldb.CheckSchema(ctx, db, tenancydb.Expected); err != nil {
		t.Errorf("the schema after Init: %v", err)
	}
}
