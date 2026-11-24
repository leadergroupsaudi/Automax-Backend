package migrations

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// MigratePermissionsPartialUniqueIndexes replaces the two predicate-less
// unique indexes on permissions, idx_permissions_code and idx_permissions_name,
// with ones scoped to WHERE deleted_at IS NULL.
//
// WHY BOTH. Permission carries soft deletes, but GORM's plain `uniqueIndex`
// tag has no predicate, so a soft-deleted row kept its code AND its name
// forever. Fixing only code would move the failure to name — EO per-type
// permissions are named "EO <verb> <key>" — and look exactly like the bug was
// not fixed. With both partial, retiring a record type (which now soft-deletes
// its permissions) releases its key for reuse.
//
// IT FAILS LOUDLY. If two LIVE rows already share a code or a name the unique
// index cannot be built, and silently skipping would leave the schema in the
// old shape while startup looked clean. The migration returns an error naming
// the duplicates and does not touch either index; Migrate treats that as a
// boot failure. It also verifies, after building, that both indexes really are
// partial.
//
// IT IS ATOMIC AND IDEMPOTENT: a transaction, so a failed build cannot leave
// the old index dropped and the new one missing, and an index already partial
// is left alone.
func MigratePermissionsPartialUniqueIndexes(db *gorm.DB) error {
	if !hasTable(db, "permissions") {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		for _, col := range []string{"code", "name"} {
			idx := "idx_permissions_" + col

			var def string
			if err := tx.Raw(`SELECT COALESCE(MAX(indexdef), '') FROM pg_indexes
				WHERE schemaname = current_schema() AND tablename = 'permissions' AND indexname = ?`, idx).
				Scan(&def).Error; err != nil {
				return fmt.Errorf("read the definition of %s: %w", idx, err)
			}
			if isPartialOnDeletedAt(def) {
				continue
			}

			var dups []struct {
				Value string
				N     int
			}
			if err := tx.Raw(`SELECT ` + col + ` AS value, COUNT(*) AS n FROM permissions
				WHERE deleted_at IS NULL GROUP BY ` + col + ` HAVING COUNT(*) > 1`).Scan(&dups).Error; err != nil {
				return fmt.Errorf("check permissions.%s for live duplicates: %w", col, err)
			}
			if len(dups) > 0 {
				parts := make([]string, 0, len(dups))
				for _, d := range dups {
					parts = append(parts, fmt.Sprintf("%q x%d", d.Value, d.N))
				}
				return fmt.Errorf("cannot make %s partial: live permissions share a %s (%s); resolve the duplicates by hand",
					idx, col, strings.Join(parts, ", "))
			}

			if err := tx.Exec(`DROP INDEX IF EXISTS ` + idx).Error; err != nil {
				return fmt.Errorf("drop %s: %w", idx, err)
			}
			if err := tx.Exec(`CREATE UNIQUE INDEX ` + idx + ` ON permissions (` + col + `) WHERE deleted_at IS NULL`).Error; err != nil {
				return fmt.Errorf("create partial %s: %w", idx, err)
			}
			if err := tx.Raw(`SELECT COALESCE(MAX(indexdef), '') FROM pg_indexes
				WHERE schemaname = current_schema() AND tablename = 'permissions' AND indexname = ?`, idx).
				Scan(&def).Error; err != nil {
				return fmt.Errorf("re-read %s: %w", idx, err)
			}
			if !isPartialOnDeletedAt(def) {
				return fmt.Errorf("%s is not partial after the migration: %q", idx, def)
			}
		}
		return nil
	})
}

func isPartialOnDeletedAt(def string) bool {
	d := strings.ToLower(def)
	return strings.Contains(d, "unique") && strings.Contains(d, "where") && strings.Contains(d, "deleted_at is null")
}
