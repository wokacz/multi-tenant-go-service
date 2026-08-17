// Package reqctx carries per-request transport facts — the peer address and
// the user agent — from the middleware chain to the handlers.
//
// It is a package of its own for the same reason problem is: internal/api
// imports internal/api/v1 to register routes, so v1 cannot import its parent,
// and a handler in v1 only ever receives a context.Context. A huma operation
// handler has no *http.Request to read these off.
//
// It holds transport facts, not identity. Who the caller is lives in
// internal/auth, which the bearer middleware fills in after verifying a token.
package reqctx

import "context"

// Client is what the service knows about where a request came from.
type Client struct {
	// IP is the client address with its ephemeral port dropped. It is the TCP
	// peer unless the peer is a trusted proxy, in which case it is the first
	// untrusted hop of X-Forwarded-For — see remoteIP in internal/api.
	IP string

	// UserAgent is unvalidated client input. It is stored and shown back to
	// the account holder, so treat it as text and nothing more.
	UserAgent string
}

type ctxKey int

const clientKey ctxKey = iota

// WithClient attaches the request's transport facts.
func WithClient(ctx context.Context, c Client) context.Context {
	return context.WithValue(ctx, clientKey, c)
}

// ClientFrom returns what WithClient attached, or a zero Client outside a
// request. The zero value is deliberately usable: the domain normalises an
// unparseable address rather than refusing to record the event.
func ClientFrom(ctx context.Context) Client {
	c, _ := ctx.Value(clientKey).(Client)

	return c
}
