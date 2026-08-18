package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/emailchange"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

// The first file on ent (see ENT.md). It was chosen for being small and for containing
// the one construct that decides whether the port is possible at all: an attempt
// counter that has to move in a single conditional UPDATE.
//
// The email-change code is the password-reset code with a different target column, so
// these four methods deliberately mirror their reset counterparts — including that
// UPDATE. Two one-time-code mechanisms that drift apart are two sets of rules to
// remember.

func (r *User) ReplaceEmailChange(ctx context.Context, change *models.EmailChange) error {
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		// Unused codes for this account go first, so asking again supersedes rather
		// than leaving two codes that both work.
		_, err := tx.EmailChange.Delete().
			Where(
				emailchange.UserID(change.UserID),
				emailchange.ConsumedAtIsNil(),
			).Exec(ctx)
		if err != nil {
			return err
		}

		created, err := tx.EmailChange.Create().
			SetUserID(change.UserID).
			SetNewEmail(change.NewEmail).
			SetCodeHash(change.CodeHash).
			SetExpiresAt(change.ExpiresAt).
			SetAttempts(change.Attempts).
			Save(ctx)
		if err != nil {
			return err
		}

		// The caller holds the struct and reads the id back off it, the way it did
		// when GORM filled it in on Create.
		change.ID = created.ID
		change.CreatedAt = created.CreatedAt
		change.UpdatedAt = created.UpdatedAt

		return nil
	})
	if err != nil {
		return fmt.Errorf("store: replace email change: %w", err)
	}

	return nil
}

func (r *User) ActiveEmailChange(ctx context.Context, userID uuid.UUID, now time.Time) (*models.EmailChange, error) {
	row, err := r.db.Ent().EmailChange.Query().
		Where(
			emailchange.UserID(userID),
			emailchange.ConsumedAtIsNil(),
			emailchange.ExpiresAtGT(now),
		).
		Order(ent.Desc(emailchange.FieldCreatedAt)).
		First(ctx)
	if err != nil {
		if isNotFound(err) {
			return nil, user.ErrNotFound
		}

		return nil, fmt.Errorf("store: active email change: %w", err)
	}

	return emailChangeModel(row), nil
}

func (r *User) FailEmailChange(ctx context.Context, changeID uuid.UUID, maxAttempts int, now time.Time) error {
	// One statement, and it has to stay one. Reading the row, adding one and writing
	// it back lets concurrent guesses all read the same value and store the same
	// value, which is how a five-attempt cap stops capping —
	// TestFailPasswordResetUnderConcurrency is the case that says so about the reset,
	// and this is its twin.
	//
	// ent can express "attempts + 1" on its own (AddAttempts), but not "spend the code
	// when this increment reaches the cap" — so the SET list is written through the
	// modifier that the sql/modifier feature exists for.
	err := r.db.Ent().EmailChange.Update().
		Where(
			emailchange.ID(changeID),
			emailchange.ConsumedAtIsNil(),
		).
		Modify(func(u *entsql.UpdateBuilder) {
			// Unqualified column names: Postgres refuses "table"."column" on the left
			// of a SET, and a qualified one on the right reads from the wrong scope.
			u.Set(emailchange.FieldAttempts, entsql.ExprFunc(func(b *entsql.Builder) {
				b.Ident(emailchange.FieldAttempts).WriteString(" + 1")
			}))

			// Every SET expression reads the pre-UPDATE row, so this sees the old
			// count and has to add the same one again.
			u.Set(emailchange.FieldConsumedAt, entsql.ExprFunc(func(b *entsql.Builder) {
				b.WriteString("CASE WHEN ").
					Ident(emailchange.FieldAttempts).
					WriteString(" + 1 >= ").Arg(maxAttempts).
					WriteString(" THEN ").Arg(now).
					WriteString("::timestamptz ELSE ").
					Ident(emailchange.FieldConsumedAt).
					WriteString(" END")
			}))
		}).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: fail email change: %w", err)
	}

	// No rows means the code was already spent, which is not an error: the caller
	// returns ErrInvalidEmailCode either way.
	return nil
}

func (r *User) ConsumeEmailChange(ctx context.Context, change *models.EmailChange, email string) error {
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		affected, err := tx.User.Update().
			Where(userIDIs(change.UserID)).
			SetEmail(email).
			Save(ctx)
		if err != nil {
			return err
		}

		if affected == 0 {
			return user.ErrNotFound
		}

		_, err = tx.EmailChange.UpdateOneID(change.ID).
			SetNillableConsumedAt(change.ConsumedAt).
			SetAttempts(change.Attempts).
			Save(ctx)

		return err
	})

	// The unique index on users.email is what decides, not a lookup: between the code
	// being sent and the code coming back, somebody else may have taken the address.
	if isUniqueViolation(err) {
		return user.ErrEmailTaken
	}

	if err != nil {
		if isNotFound(err) || errors.Is(err, user.ErrNotFound) {
			return user.ErrNotFound
		}

		return fmt.Errorf("store: consume email change: %w", err)
	}

	return nil
}

// emailChangeModel maps the entity onto the struct the domain reads.
//
// The mapping is the price of keeping ent inside the store — see ENT.md, D1 — and it
// buys the thing that makes this migration reviewable: the domain, the in-memory fake
// and every contract case stay exactly as they were, so they can say whether the
// behaviour changed.
func emailChangeModel(row *ent.EmailChange) *models.EmailChange {
	out := &models.EmailChange{
		UserID:     row.UserID,
		NewEmail:   row.NewEmail,
		CodeHash:   row.CodeHash,
		ExpiresAt:  row.ExpiresAt,
		Attempts:   row.Attempts,
		ConsumedAt: row.ConsumedAt,
	}

	out.ID = row.ID
	out.CreatedAt = row.CreatedAt
	out.UpdatedAt = row.UpdatedAt

	return out
}
