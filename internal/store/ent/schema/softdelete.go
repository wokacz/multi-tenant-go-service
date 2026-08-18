package schema

import (
	"context"
	"fmt"
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/sql"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/mixin"

	gen "github.com/wokacz/multi-tenant-go-service/internal/store/ent"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/hook"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/intercept"
)

// SoftDelete retires a row instead of removing it, and hides retired rows from every
// read.
//
// Two halves, and they are different mechanisms for a reason. The interceptor adds
// "deleted_at IS NULL" to queries, so a read cannot forget it — under GORM that was
// the scope on gorm.DeletedAt, and the one place it did not apply (a condition hanging
// off a LEFT JOIN) is where deleted accounts stayed visible for months. The hook turns
// a delete into an update, so nothing has to remember to write a timestamp instead of
// issuing a DELETE.
//
// Neither is optional and both are escapable, through SkipSoftDelete. That escape is
// not a loophole, it is a requirement: RemoveMember has to work on a membership whose
// account is gone — every other method reports such a row as not found, so refusing
// here too would leave it in the organization with no way out — and the seeder's reset
// has to delete rows it retired earlier.
//
// What is *not* here: models.ErrProtected. A delete hook receives a predicate, not a
// row, so it cannot see whether is_protected is set without a query of its own. The
// repository loads the organization before deleting it — it already did under GORM, for
// exactly this reason — and that is where the refusal belongs.
type SoftDelete struct {
	mixin.Schema
}

func (SoftDelete) Fields() []ent.Field {
	return []ent.Field{
		field.Time("deleted_at").
			Optional().
			Nillable(),

		// The default organization carries this. An installation that lost its only
		// organization has no working accounts and no screen to undo it from.
		field.Bool("is_protected").
			Default(false),
	}
}

// skipKey marks a context that wants deleted rows.
type skipKey struct{}

// SkipSoftDelete returns a context in which reads see retired rows and a delete really
// deletes.
//
// Deliberately verbose at the call site: a query that wants to see retired rows is
// making a claim about what it is for, and that claim should be visible in the line
// rather than in a package-level setting.
func SkipSoftDelete(parent context.Context) context.Context {
	return context.WithValue(parent, skipKey{}, true)
}

func skipped(ctx context.Context) bool {
	skip, _ := ctx.Value(skipKey{}).(bool)

	return skip
}

func (d SoftDelete) Interceptors() []ent.Interceptor {
	return []ent.Interceptor{
		intercept.TraverseFunc(func(ctx context.Context, q intercept.Query) error {
			if skipped(ctx) {
				return nil
			}

			d.P(q)

			return nil
		}),
	}
}

func (d SoftDelete) Hooks() []ent.Hook {
	return []ent.Hook{
		hook.On(
			func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
					if skipped(ctx) {
						return next.Mutate(ctx, m)
					}

					mx, ok := m.(interface {
						SetOp(ent.Op)
						Client() *gen.Client
						SetDeletedAt(time.Time)
						WhereP(...func(*sql.Selector))
					})
					if !ok {
						return nil, fmt.Errorf("schema: unexpected mutation type %T", m)
					}

					// Retiring a row that is already retired would move its timestamp,
					// rewriting when it happened.
					d.P(mx)
					mx.SetOp(ent.OpUpdate)
					mx.SetDeletedAt(time.Now().UTC())

					return mx.Client().Mutate(ctx, m)
				})
			},
			ent.OpDeleteOne|ent.OpDelete,
		),
	}
}

// P is the predicate both halves share: only live rows.
func (d SoftDelete) P(w interface{ WhereP(...func(*sql.Selector)) }) {
	w.WhereP(sql.FieldIsNull(d.Fields()[0].Descriptor().Name))
}
