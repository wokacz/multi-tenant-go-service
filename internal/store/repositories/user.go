package repositories

import (
	"context"
	"fmt"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/store"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/device"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/passwordreset"
	entuser "github.com/wokacz/multi-tenant-go-service/internal/store/ent/user"
)

// User implements user.Repository. The interface it satisfies is declared in
// internal/domain/user, so the dependency points inwards: the domain does not
// know this type exists.
type User struct {
	db *store.DB
}

func NewUser(db *store.DB) *User {
	return &User{db: db}
}

// Compile-time check that this still satisfies the interface the domain
// declares. Without it, a drifting signature would only surface at the call
// site in main, far from either definition.
var _ user.Repository = (*User)(nil)

func (r *User) Create(ctx context.Context, u *ent.User) error {
	create := r.db.Ent().User.Create().
		SetName(u.Name).
		SetEmail(u.Email).
		SetPasswordHash(u.PasswordHash).
		SetLocale(u.Locale).
		SetSessionEpoch(u.SessionEpoch).
		SetTwoFactorEnabled(u.TwoFactorEnabled).
		SetNillableSuspendedAt(u.SuspendedAt).
		SetIsProtected(u.IsProtected)
	if u.ID != uuid.Nil {
		create = create.SetID(u.ID)
	}

	created, err := create.Save(ctx)
	if err != nil {
		if isUniqueViolation(err) {
			return user.ErrEmailTaken
		}

		return fmt.Errorf("store: create user: %w", err)
	}

	// The caller reads the generated fields back off the struct it passed in.
	u.ID = created.ID
	u.CreatedAt = created.CreatedAt
	u.UpdatedAt = created.UpdatedAt
	u.SessionEpoch = created.SessionEpoch
	u.TwoFactorEnabled = created.TwoFactorEnabled
	u.IsProtected = created.IsProtected

	return nil
}

func (r *User) ByID(ctx context.Context, id uuid.UUID) (*ent.User, error) {
	row, err := r.db.Ent().User.Get(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return nil, user.ErrNotFound
		}

		return nil, fmt.Errorf("store: user by id: %w", err)
	}

	return row, nil
}

// All lists live accounts, newest first. UUIDv7 is time-ordered, so ordering by
// the primary key is the same order as by creation and costs no extra index.
func (r *User) All(ctx context.Context, limit, offset int) ([]ent.User, error) {
	rows, err := r.db.Ent().User.Query().
		Order(ent.Desc(entuser.FieldID)).
		Limit(limit).
		Offset(offset).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: all users: %w", err)
	}

	out := make([]ent.User, 0, len(rows))
	for _, row := range rows {
		out = append(out, *row)
	}

	return out, nil
}

// Delete soft deletes an account.
//
// The row is loaded first so the refusal on is_protected and the device revoke
// both see a real account. ent's delete hook receives a predicate, not a row —
// it can retire the user, but it cannot see whose devices to revoke, and it
// cannot see is_protected without a query of its own. A bulk delete would skip
// both, and the devices would stay trusted and usable after the account was gone.
func (r *User) Delete(ctx context.Context, userID uuid.UUID) error {
	u, err := r.ByID(ctx, userID)
	if err != nil {
		return err
	}

	if err := u.RefuseDelete(); err != nil {
		return err
	}

	err = r.withTx(ctx, func(tx *ent.Tx) error {
		// Soft delete does not fire the FK cascade, so without this the devices
		// survive as trusted after the account is gone.
		_, err := tx.Device.Update().
			Where(
				device.UserID(userID),
				device.RevokedAtIsNil(),
			).
			SetRevokedAt(time.Now().UTC()).
			Save(ctx)
		if err != nil {
			return err
		}

		return tx.User.DeleteOneID(userID).Exec(ctx)
	})
	if err != nil {
		if isNotFound(err) {
			return user.ErrNotFound
		}

		return fmt.Errorf("store: delete user: %w", err)
	}

	return nil
}

// UpdateProfile writes the two fields an account owner may change about
// themselves.
//
// Both columns are always written, including an empty locale. That empty string
// is the whole point of the statement: it means "no preference" and puts the
// account back to negotiating per request. ClearLocale would store NULL, which
// reads back as "". SetLocale("") is the write TestUpdateProfileWritesBothColumns
// exists for: omitting a zero value would silently keep the old locale.
func (r *User) UpdateProfile(ctx context.Context, userID uuid.UUID, name, locale string) error {
	affected, err := r.db.Ent().User.Update().
		Where(entuser.ID(userID), entuser.DeletedAtIsNil()).
		SetName(name).
		SetLocale(locale).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("store: update profile: %w", err)
	}

	if affected == 0 {
		return user.ErrNotFound
	}

	return nil
}

func (r *User) SetPassword(ctx context.Context, userID uuid.UUID, passwordHash string) error {
	return r.bumpEpoch(ctx, "set password", userID, func(u *ent.UserUpdate) *ent.UserUpdate {
		return u.SetPasswordHash(passwordHash)
	})
}

func (r *User) BumpSessionEpoch(ctx context.Context, userID uuid.UUID) error {
	return r.bumpEpoch(ctx, "bump session epoch", userID, nil)
}

// bumpEpoch applies updates and moves session_epoch in the same statement.
//
// The increment is an expression rather than a read and a write: two concurrent
// changes that both read 4 would both write 5, and a token issued under 4 would
// survive one of them. AddSessionEpoch is that expression.
func (r *User) bumpEpoch(ctx context.Context, op string, userID uuid.UUID, extra func(*ent.UserUpdate) *ent.UserUpdate) error {
	update := r.db.Ent().User.Update().
		Where(entuser.ID(userID), entuser.DeletedAtIsNil()).
		AddSessionEpoch(1)
	if extra != nil {
		update = extra(update)
	}

	affected, err := update.Save(ctx)
	if err != nil {
		return fmt.Errorf("store: %s: %w", op, err)
	}

	if affected == 0 {
		return user.ErrNotFound
	}

	return nil
}

// SetSuspended blocks or unblocks an account.
//
// Suspending bumps the session epoch in the same statement, so tokens already
// issued stop working on the next request. Doing it in two statements would
// leave a window in which a suspended account still had a usable token, which
// is precisely the window an administrator is trying to close.
//
// Restoring must not move the epoch: that would sign out somebody who was never
// suspended in between. Clearing the timestamp is ClearSuspendedAt — SetNillable
// with a nil pointer does not write NULL, it does nothing.
func (r *User) SetSuspended(ctx context.Context, userID uuid.UUID, at *time.Time) error {
	update := r.db.Ent().User.Update().
		Where(entuser.ID(userID), entuser.DeletedAtIsNil())
	if at != nil {
		update = update.SetSuspendedAt(*at).AddSessionEpoch(1)
	} else {
		update = update.ClearSuspendedAt()
	}

	affected, err := update.Save(ctx)
	if err != nil {
		return fmt.Errorf("store: set suspended: %w", err)
	}

	if affected == 0 {
		return user.ErrNotFound
	}

	return nil
}

func (r *User) ByEmail(ctx context.Context, email string) (*ent.User, error) {
	row, err := r.db.Ent().User.Query().Where(entuser.Email(email)).First(ctx)
	if err != nil {
		if isNotFound(err) {
			return nil, user.ErrNotFound
		}

		return nil, fmt.Errorf("store: user by email: %w", err)
	}

	return row, nil
}

func (r *User) ReplacePasswordReset(ctx context.Context, reset *ent.PasswordReset) error {
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		// Unused codes for this account go first, so asking again supersedes rather
		// than leaving two codes that both work.
		_, err := tx.PasswordReset.Delete().
			Where(
				passwordreset.UserID(reset.UserID),
				passwordreset.ConsumedAtIsNil(),
			).Exec(ctx)
		if err != nil {
			return err
		}

		created, err := tx.PasswordReset.Create().
			SetUserID(reset.UserID).
			SetCodeHash(reset.CodeHash).
			SetExpiresAt(reset.ExpiresAt).
			SetAttempts(reset.Attempts).
			Save(ctx)
		if err != nil {
			return err
		}

		reset.ID = created.ID
		reset.CreatedAt = created.CreatedAt
		reset.UpdatedAt = created.UpdatedAt

		return nil
	})
	if err != nil {
		return fmt.Errorf("store: replace password reset: %w", err)
	}

	return nil
}

func (r *User) ActivePasswordReset(ctx context.Context, userID uuid.UUID, now time.Time) (*ent.PasswordReset, error) {
	row, err := r.db.Ent().PasswordReset.Query().
		Where(
			passwordreset.UserID(userID),
			passwordreset.ConsumedAtIsNil(),
			passwordreset.ExpiresAtGT(now),
		).
		Order(ent.Desc(passwordreset.FieldCreatedAt)).
		First(ctx)
	if err != nil {
		if isNotFound(err) {
			return nil, user.ErrNotFound
		}

		return nil, fmt.Errorf("store: active password reset: %w", err)
	}

	return row, nil
}

// FailPasswordReset moves the attempt counter in a single statement.
//
// Reading the row, adding one and saving it back is the obvious shape and the
// wrong one: two guesses that overlap both read the same count and both write
// the same count, so five concurrent attempts leave the counter at one. Worse,
// a slow writer could put back a consumed_at that another request had just set
// and reopen a code that was already spent. Here the increment and the decision
// to spend the code are one UPDATE, and the WHERE keeps a spent code spent.
func (r *User) FailPasswordReset(ctx context.Context, resetID uuid.UUID, maxAttempts int, now time.Time) error {
	err := r.db.Ent().PasswordReset.Update().
		Where(
			passwordreset.ID(resetID),
			passwordreset.ConsumedAtIsNil(),
		).
		Modify(func(u *entsql.UpdateBuilder) {
			u.Set(passwordreset.FieldAttempts, entsql.ExprFunc(func(b *entsql.Builder) {
				b.Ident(passwordreset.FieldAttempts).WriteString(" + 1")
			}))

			// Every SET expression reads the pre-UPDATE row, so this sees the old
			// count and has to add the same one again.
			u.Set(passwordreset.FieldConsumedAt, entsql.ExprFunc(func(b *entsql.Builder) {
				b.WriteString("CASE WHEN ").
					Ident(passwordreset.FieldAttempts).
					WriteString(" + 1 >= ").Arg(maxAttempts).
					WriteString(" THEN ").Arg(now).
					WriteString("::timestamptz ELSE ").
					Ident(passwordreset.FieldConsumedAt).
					WriteString(" END")
			}))
		}).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("store: fail password reset: %w", err)
	}

	// No rows means the code was already spent, which is not an error: the
	// caller is about to return ErrInvalidResetCode either way.
	return nil
}

func (r *User) ConsumePasswordReset(ctx context.Context, reset *ent.PasswordReset, passwordHash string) error {
	err := r.withTx(ctx, func(tx *ent.Tx) error {
		_, err := tx.User.Update().
			Where(entuser.ID(reset.UserID), entuser.DeletedAtIsNil()).
			SetPasswordHash(passwordHash).
			AddSessionEpoch(1).
			Save(ctx)
		if err != nil {
			return err
		}

		_, err = tx.PasswordReset.UpdateOneID(reset.ID).
			SetNillableConsumedAt(reset.ConsumedAt).
			SetAttempts(reset.Attempts).
			Save(ctx)

		return err
	})
	if err != nil {
		if isNotFound(err) {
			return user.ErrNotFound
		}

		return fmt.Errorf("store: consume password reset: %w", err)
	}

	return nil
}
