package migrations

import (
	"gorm.io/gorm"
)

// MigrateUserTwoFactorEnabled adds the two_factor_enabled column to the users table
func MigrateUserTwoFactorEnabled(db *gorm.DB) error {
	// Check if column exists before adding
	if !db.Migrator().HasColumn(&User2FA{}, "two_factor_enabled") {
		migrationSQL := `
			-- Add two_factor_enabled column to users table
			ALTER TABLE users ADD COLUMN IF NOT EXISTS two_factor_enabled BOOLEAN DEFAULT false;
		`
		return db.Exec(migrationSQL).Error
	}
	return nil
}

// User2FA is a minimal struct for migration purposes
type User2FA struct {
	ID string `gorm:"type:uuid;primary_key"`
}
