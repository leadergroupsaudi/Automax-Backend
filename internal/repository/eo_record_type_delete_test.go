package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/automax/backend/internal/database/migrations"
	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func eoCount(t *testing.T, db *gorm.DB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(query, args...).Scan(&n).Error; err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// eoTypeGrants counts role_permissions rows naming any permission (deleted or
// not) of the key's three codes.
func eoTypeGrants(t *testing.T, db *gorm.DB, key string) int64 {
	return eoCount(t, db, `SELECT COUNT(*) FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
		WHERE p.code IN ?`, []string{
		models.EOTypePermissionCode(key, models.EOTypePermissionView),
		models.EOTypePermissionCode(key, models.EOTypePermissionCreate),
		models.EOTypePermissionCode(key, models.EOTypePermissionUpdate),
	})
}

// TestDeleteEORecordType_ReleasesPermissionsAndGrants: deleting a type
// soft-deletes its three permissions and removes their six grants, and touches
// nothing else in the product-wide permissions table.
func TestDeleteEORecordType_ReleasesPermissionsAndGrants(t *testing.T) {
	db := eoRecordDB(t)
	types := NewEORecordTypeRepository(db)
	ctx := context.Background()

	// Bystanders: a product permission granted to a role, and the codes of a
	// type whose key differs only by what a LIKE wildcard would match ("eo_a"
	// vs "eoxa"), and one that merely shares a prefix.
	product := models.Permission{ID: uuid.New(), Name: "Create users", Code: "users.create", Module: "users"}
	if err := db.Create(&product).Error; err != nil {
		t.Fatal(err)
	}
	var role models.Role
	if err := db.Where("code = ?", models.EOTypePermissionRoleCodes[0]).First(&role).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&role).Association("Permissions").Append([]models.Permission{product}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"eoxa", "eo_a_extra"} {
		if err := types.Create(ctx, &models.EORecordType{ID: uuid.New(), Key: key, Name: key, IsActive: true}); err != nil {
			t.Fatalf("create bystander %s: %v", key, err)
		}
	}

	ty := &models.EORecordType{ID: uuid.New(), Key: "eo_a", Name: "A", IsActive: true}
	if err := types.Create(ctx, ty); err != nil {
		t.Fatalf("create: %v", err)
	}
	if n := eoCountTypeCodes(t, db, "eo_a"); n != 3 {
		t.Fatalf("%d live codes before delete, want 3", n)
	}
	if n := eoTypeGrants(t, db, "eo_a"); n != int64(3*len(models.EOTypePermissionRoleCodes)) {
		t.Fatalf("%d grants before delete, want %d", n, 3*len(models.EOTypePermissionRoleCodes))
	}
	liveBefore := eoCount(t, db, `SELECT COUNT(*) FROM permissions WHERE deleted_at IS NULL`)
	grantsBefore := eoCount(t, db, `SELECT COUNT(*) FROM role_permissions`)

	if err := types.Delete(ctx, ty.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if n := eoCountTypeCodes(t, db, "eo_a"); n != 0 {
		t.Errorf("%d live codes after delete, want 0", n)
	}
	if n := eoCount(t, db, `SELECT COUNT(*) FROM permissions WHERE code LIKE 'eo:eo\_a:%' AND deleted_at IS NOT NULL`); n != 3 {
		t.Errorf("%d soft-deleted codes, want 3 (soft, not hard, delete)", n)
	}
	if n := eoTypeGrants(t, db, "eo_a"); n != 0 {
		t.Errorf("%d grants after delete, want 0", n)
	}
	// Exactly this type's rows moved: 3 permissions, 6 grants.
	if got := eoCount(t, db, `SELECT COUNT(*) FROM permissions WHERE deleted_at IS NULL`); got != liveBefore-3 {
		t.Errorf("live permissions %d -> %d, want exactly 3 fewer", liveBefore, got)
	}
	if got := eoCount(t, db, `SELECT COUNT(*) FROM role_permissions`); got != grantsBefore-6 {
		t.Errorf("grants %d -> %d, want exactly 6 fewer", grantsBefore, got)
	}
	for _, key := range []string{"eoxa", "eo_a_extra"} {
		if n := eoCountTypeCodes(t, db, key); n != 3 {
			t.Errorf("bystander %s has %d live codes, want 3", key, n)
		}
		if n := eoTypeGrants(t, db, key); n != 6 {
			t.Errorf("bystander %s has %d grants, want 6", key, n)
		}
	}
	if n := eoCount(t, db, `SELECT COUNT(*) FROM permissions WHERE code = 'users.create' AND deleted_at IS NULL`); n != 1 {
		t.Errorf("the product permission users.create was touched")
	}
	if n := eoCount(t, db, `SELECT COUNT(*) FROM role_permissions WHERE permission_id = ?`, product.ID); n != 1 {
		t.Errorf("the product permission's grant was touched")
	}
	if n := eoCount(t, db, `SELECT COUNT(*) FROM role_permissions rp LEFT JOIN permissions p ON p.id = rp.permission_id
		WHERE p.id IS NULL OR p.deleted_at IS NOT NULL`); n != 0 {
		t.Errorf("%d orphan grants after delete, want 0", n)
	}
}

// TestEORecordType_KeyCanBeReusedAfterDelete: create, delete, re-create under
// the same key. It fails on the permissions table's unique indexes if either
// idx_permissions_code or idx_permissions_name is predicate-less.
func TestEORecordType_KeyCanBeReusedAfterDelete(t *testing.T) {
	db := eoRecordDB(t)
	types := NewEORecordTypeRepository(db)
	ctx := context.Background()

	for round := 1; round <= 2; round++ {
		ty := &models.EORecordType{ID: uuid.New(), Key: "eo_reused", Name: "Reused", IsActive: true}
		if err := types.Create(ctx, ty); err != nil {
			t.Fatalf("round %d: create under the reused key: %v", round, err)
		}
		if n := eoCountTypeCodes(t, db, "eo_reused"); n != 3 {
			t.Fatalf("round %d: %d live codes, want 3 fresh ones", round, n)
		}
		if n := eoTypeGrants(t, db, "eo_reused"); n != 6 {
			t.Fatalf("round %d: %d grants on the live codes, want 6", round, n)
		}
		if err := types.Delete(ctx, ty.ID); err != nil {
			t.Fatalf("round %d: delete: %v", round, err)
		}
	}
	if n := eoCount(t, db, `SELECT COUNT(*) FROM permissions WHERE code LIKE 'eo:eo\_reused:%'`); n != 6 {
		t.Errorf("%d rows (live and deleted), want 6: two retired generations", n)
	}
}

// TestEORecordType_AbstractToConcreteOnRetiredKeyGetsFreshCodes: the Update
// path that adopts a key must also write codes beside a retired type's
// soft-deleted ones, or the type is invisible to everyone but a super admin.
func TestEORecordType_AbstractToConcreteOnRetiredKeyGetsFreshCodes(t *testing.T) {
	db := eoRecordDB(t)
	types := NewEORecordTypeRepository(db)
	ctx := context.Background()

	old := &models.EORecordType{ID: uuid.New(), Key: "eo_k", Name: "K", IsActive: true}
	if err := types.Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := types.Delete(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	abs := &models.EORecordType{ID: uuid.New(), Key: "eo_k", Name: "K2", IsAbstract: true, IsActive: true}
	if err := types.Create(ctx, abs); err != nil {
		t.Fatalf("create abstract: %v", err)
	}
	abs.IsAbstract = false
	if err := types.Update(ctx, abs); err != nil {
		t.Fatalf("make concrete: %v", err)
	}
	if n := eoCountTypeCodes(t, db, "eo_k"); n != 3 {
		t.Errorf("%d live codes, want 3", n)
	}
}

// TestMigratePermissionsPartialUniqueIndexes runs the migration over the old
// shape: it must make BOTH indexes partial, be idempotent, and FAIL (changing
// nothing) when live duplicates exist.
func TestMigratePermissionsPartialUniqueIndexes(t *testing.T) {
	legacy := func(t *testing.T) *gorm.DB {
		db := eoTestDB(t)
		for _, q := range []string{
			`CREATE TABLE permissions (id uuid PRIMARY KEY, name text, code text, deleted_at timestamptz)`,
			`CREATE UNIQUE INDEX idx_permissions_code ON permissions (code)`,
			`CREATE UNIQUE INDEX idx_permissions_name ON permissions (name)`,
		} {
			if err := db.Exec(q).Error; err != nil {
				t.Fatal(err)
			}
		}
		return db
	}
	insert := func(t *testing.T, db *gorm.DB, name, code string, deleted bool) error {
		d := "NULL"
		if deleted {
			d = "now()"
		}
		return db.Exec(`INSERT INTO permissions VALUES (?, ?, ?, `+d+`)`, uuid.New(), name, code).Error
	}

	t.Run("makes both partial and allows reuse beside a deleted row", func(t *testing.T) {
		db := legacy(t)
		if err := insert(t, db, "n", "c", true); err != nil {
			t.Fatal(err)
		}
		if err := insert(t, db, "n", "c", false); err == nil {
			t.Fatal("legacy shape accepted a reuse; the fixture is not the old shape")
		}
		if err := migrations.MigratePermissionsPartialUniqueIndexes(db); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		if err := insert(t, db, "n", "c", false); err != nil {
			t.Errorf("reuse beside a deleted row after migration: %v", err)
		}
		if err := insert(t, db, "n2", "c", false); err == nil {
			t.Error("a second LIVE row with the same code was accepted")
		}
		if err := insert(t, db, "n", "c2", false); err == nil {
			t.Error("a second LIVE row with the same name was accepted")
		}
		if err := migrations.MigratePermissionsPartialUniqueIndexes(db); err != nil {
			t.Errorf("second run is not idempotent: %v", err)
		}
	})

	for _, dup := range []string{"code", "name"} {
		t.Run("fails loudly on live duplicate "+dup, func(t *testing.T) {
			db := eoTestDB(t)
			if err := db.Exec(`CREATE TABLE permissions (id uuid PRIMARY KEY, name text, code text, deleted_at timestamptz)`).Error; err != nil {
				t.Fatal(err)
			}
			if err := insert(t, db, "a", "x", false); err != nil {
				t.Fatal(err)
			}
			n2, c2 := "b", "y"
			if dup == "name" {
				n2, c2 = "a", "y"
			} else {
				n2, c2 = "b", "x"
			}
			if err := insert(t, db, n2, c2, false); err != nil {
				t.Fatal(err)
			}
			err := migrations.MigratePermissionsPartialUniqueIndexes(db)
			if err == nil {
				t.Fatal("migration returned nil over live duplicate " + dup)
			}
			if !strings.Contains(err.Error(), "share a "+dup) {
				t.Errorf("the error does not name the duplicated %s: %v", dup, err)
			}
		})
	}
}
