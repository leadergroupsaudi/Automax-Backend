package repository

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// EOSequenceRepository hands out the sequence numbers behind EO record
// references.
type EOSequenceRepository interface {
	// NextInScope returns the next number (1, 2, 3, ...) for the scope, which
	// services.EOSequenceScope composes. It is safe under any concurrency, and
	// the counter never goes backwards, so a number is never issued twice even
	// if the record that used it is later soft-deleted.
	NextInScope(ctx context.Context, scope string) (int, error)
}

type eoSequenceRepository struct {
	db *gorm.DB
}

func NewEOSequenceRepository(db *gorm.DB) EOSequenceRepository {
	return &eoSequenceRepository{db: db}
}

// NextInScope is a single upsert. The first caller inserts the row at 1; every
// later one hits the primary key, and ON CONFLICT DO UPDATE takes the row lock
// and re-reads the committed value before adding one, so concurrent callers
// queue on the row and each returns a distinct value. There is no separate read
// to race with.
func (r *eoSequenceRepository) NextInScope(ctx context.Context, scope string) (int, error) {
	var seq int
	err := r.db.WithContext(ctx).Raw(
		`INSERT INTO eo_reference_sequences (scope, seq) VALUES (?, 1)
		 ON CONFLICT (scope) DO UPDATE SET seq = eo_reference_sequences.seq + 1
		 RETURNING seq`, scope,
	).Scan(&seq).Error
	if err != nil {
		return 0, fmt.Errorf("next sequence in scope %q: %w", scope, err)
	}
	return seq, nil
}
