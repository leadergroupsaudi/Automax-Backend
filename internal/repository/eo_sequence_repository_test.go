package repository

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/automax/backend/internal/database/migrations"
)

// eoSequenceTestDB is eoTestDB plus the sequence table and a second handle with
// a real connection pool on the same schema. eoTestDB pins its pool to one
// connection, which would serialise the goroutines and hide a race.
func eoSequenceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	single := eoTestDB(t)
	if err := migrations.MigrateEOPlatform(single); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var schema string
	if err := single.Raw(`SELECT current_schema()`).Scan(&schema).Error; err != nil {
		t.Fatalf("current_schema: %v", err)
	}
	dsn := os.Getenv("AUTOMAX_TEST_DB_DSN")
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	pooled, err := gorm.Open(postgres.Open(dsn+sep+"search_path="+schema), &gorm.Config{})
	if err != nil {
		t.Fatalf("open pooled db: %v", err)
	}
	sqlDB, _ := pooled.DB()
	sqlDB.SetMaxOpenConns(20)
	t.Cleanup(func() { sqlDB.Close() })
	return pooled
}

func TestNextInScopeIsUniqueUnderConcurrency(t *testing.T) {
	db := eoSequenceTestDB(t)
	repo := NewEOSequenceRepository(db)
	const n = 20
	const scope = "IN-YYYY-NNNN-R#|IN-2026-0001"

	var wg sync.WaitGroup
	start := make(chan struct{})
	got := make([]int, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			got[i], errs[i] = repo.NextInScope(context.Background(), scope)
		}(i)
	}
	close(start)
	wg.Wait()

	seen := map[int]bool{}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if seen[got[i]] {
			t.Errorf("duplicate sequence value %d", got[i])
		}
		seen[got[i]] = true
	}
	if len(seen) != n {
		t.Fatalf("got %d distinct values, want %d", len(seen), n)
	}
	for v := 1; v <= n; v++ {
		if !seen[v] {
			t.Errorf("value %d missing: values must be gapless 1..%d", v, n)
		}
	}
}

func TestNextInScopeCountsEachScopeIndependently(t *testing.T) {
	db := eoSequenceTestDB(t)
	repo := NewEOSequenceRepository(db)
	ctx := context.Background()

	for _, step := range []struct {
		scope string
		want  int
	}{
		{"a|1", 1}, {"a|1", 2}, {"a|2", 1}, {"b|1", 1}, {"a|1", 3}, {"a|2", 2},
	} {
		got, err := repo.NextInScope(ctx, step.scope)
		if err != nil {
			t.Fatalf("NextInScope(%q): %v", step.scope, err)
		}
		if got != step.want {
			t.Errorf("NextInScope(%q) = %d, want %d", step.scope, got, step.want)
		}
	}
}
