package auth

import (
	"context"

	"github.com/google/uuid"
)

type ctxKey int

const sessionKey ctxKey = iota

// WithSession stores the verified session on the request context. Handlers read
// it back rather than re-parsing the Authorization header, so the token is
// verified in exactly one place.
func WithSession(ctx context.Context, sess Session) context.Context {
	return context.WithValue(ctx, sessionKey, sess)
}

// SessionFrom returns the session installed by WithSession. The second result
// is false on any operation that did not go through bearer authentication, so
// a handler cannot mistake an anonymous request for a nil-uuid user.
func SessionFrom(ctx context.Context) (Session, bool) {
	sess, ok := ctx.Value(sessionKey).(Session)

	return sess, ok && sess.UserID != uuid.Nil && sess.DeviceID != uuid.Nil
}
