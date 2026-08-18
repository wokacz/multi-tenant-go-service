package repositories

import (
	"context"
	"errors"
	"fmt"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
	entuser "github.com/wokacz/multi-tenant-go-service/internal/store/ent/user"
)

// withEntTx runs fn in a transaction and cleans up after it either way.
//
// It exists because ent's transaction API is three steps — begin, use, commit or roll
// back — and a repository that writes two rows should not spend four lines on the
// bookkeeping each time. GORM's Transaction did the same thing; this is the same shape
// so the ported methods read like the ones still to come.
//
// A rollback failure is joined rather than swallowed: it means the transaction may
// still be open, which matters more than the error that caused the rollback.
func withEntTx(ctx context.Context, client *ent.Client, fn func(tx *ent.Tx) error) error {
	tx, err := client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}

	if err := fn(tx); err != nil {
		if rerr := tx.Rollback(); rerr != nil {
			return errors.Join(err, fmt.Errorf("rollback: %w", rerr))
		}

		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// withTx is withEntTx on this repository's client.
func (r *User) withTx(ctx context.Context, fn func(tx *ent.Tx) error) error {
	return withEntTx(ctx, r.db.Ent(), fn)
}

// userIDIs is the predicate for one account.
//
// Named rather than inlined because entuser.ID reads as "the id field" at a call site
// where the surrounding code is about an email change, and a predicate on the wrong
// table compiles.
func userIDIs(id uuid.UUID) func(*entsql.Selector) {
	return entuser.ID(id)
}
