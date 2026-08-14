package auth

import (
	"context"

	"github.com/google/uuid"
)

type ctxKey int

const userIDKey ctxKey = iota

// WithUserID stores the authenticated subject on the request context.
func WithUserID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, userIDKey, id)
}

// UserIDFrom returns the subject installed by WithUserID.
func UserIDFrom(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(userIDKey).(uuid.UUID)
	return id, ok && id != uuid.Nil
}
