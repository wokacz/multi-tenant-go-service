package v1

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wokacz/multi-tenant-go-service/internal/api/problem"
	"github.com/wokacz/multi-tenant-go-service/internal/api/reqctx"
	"github.com/wokacz/multi-tenant-go-service/internal/auth"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/mail"
	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

// deviceTokenHeader is how a client says "you have seen me before". It is a
// header rather than a body field so the same value can be sent on both
// sign-in steps without changing either schema.
const deviceTokenHeader = "X-Device-Token"

type CreateSessionInput struct {
	DeviceToken string `header:"X-Device-Token" doc:"Device token from a previous sign-in, if any"`
	Body        CreateSessionRequest
}

type VerifySessionInput struct {
	DeviceToken string `header:"X-Device-Token" required:"true" doc:"Device token the code was issued for"`
	Body        VerifySessionRequest
}

// CreateSessionOutput carries an explicit Status because this operation has two
// successful outcomes: 201 with a token, or 202 when a code was emailed and the
// sign-in is not finished yet.
type CreateSessionOutput struct {
	Status int
	Body   SessionResponse
}

type sessionHandlers struct {
	users  *user.Service
	tokens *auth.Signer
	mail   mail.Sender
	log    *slog.Logger
}

func registerSessions(api huma.API, deps Deps) {
	h := &sessionHandlers{users: deps.Users, tokens: deps.Tokens, mail: deps.Mail, log: deps.Log}

	huma.Register(api, huma.Operation{
		OperationID: "create-session",
		Method:      http.MethodPost,
		Path:        Prefix + "/sessions",
		Summary:     "Sign in",
		Description: "Exchanges an email and password for a Bearer token. Wrong " +
			"passwords and unknown addresses share one error. When the account " +
			"has two-factor on and the " + deviceTokenHeader + " is missing or " +
			"names an untrusted device, the answer is 202 with " +
			"two_factor_required and a code goes out by email; finish at " +
			"POST /v1/sessions/verify.",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusCreated,
		Errors: []int{
			http.StatusUnauthorized,
			http.StatusForbidden,
			http.StatusUnprocessableEntity,
			http.StatusTooManyRequests,
		},
	}, h.create)

	huma.Register(api, huma.Operation{
		OperationID: "verify-session",
		Method:      http.MethodPost,
		Path:        Prefix + "/sessions/verify",
		Summary:     "Finish a two-factor sign-in",
		Description: "Spends the emailed code and issues the token. The " +
			deviceTokenHeader + " must be the one returned by the sign-in that " +
			"raised the challenge — a code authorises one device, not one " +
			"mailbox. Succeeding also trusts that device, so later sign-ins " +
			"from it skip the code. Every way of getting it wrong shares one " +
			"error.",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusCreated,
		Errors: []int{
			http.StatusUnauthorized,
			http.StatusForbidden,
			http.StatusUnprocessableEntity,
			http.StatusTooManyRequests,
		},
	}, h.verify)
}

func (h *sessionHandlers) create(ctx context.Context, in *CreateSessionInput) (*CreateSessionOutput, error) {
	result, err := h.users.SignIn(ctx, in.Body.Email, in.Body.Password, signInContext(ctx, in.DeviceToken))
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	if result.Challenged {
		// Unlike the password-reset request, a delivery failure is reported.
		// The caller already proved the password, so a 5xx here tells them
		// nothing about the account that they did not already know, and
		// answering 202 for a code that was never sent would leave them
		// waiting on a mail that is not coming.
		if err := h.deliverCode(ctx, result.User.Email, result.Code); err != nil {
			return nil, problem.Error(ctx, err)
		}

		return &CreateSessionOutput{
			Status: http.StatusAccepted,
			Body: SessionResponse{
				TwoFactorRequired: true,
				DeviceToken:       result.DeviceToken,
			},
		}, nil
	}

	return h.issue(ctx, result.User, result.Device, result.DeviceToken)
}

func (h *sessionHandlers) verify(ctx context.Context, in *VerifySessionInput) (*CreateSessionOutput, error) {
	u, device, err := h.users.VerifyTwoFactor(ctx, in.Body.Email, in.Body.Code, signInContext(ctx, in.DeviceToken))
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	// No device token is returned: the client already holds the one it sent.
	return h.issue(ctx, u, device, "")
}

// issue signs the token for a completed sign-in.
func (h *sessionHandlers) issue(
	ctx context.Context,
	u *models.User,
	device *models.Device,
	deviceToken string,
) (*CreateSessionOutput, error) {
	if h.tokens == nil {
		return nil, problem.Error(ctx, user.ErrUnauthorized)
	}

	token, expires, err := h.tokens.Issue(auth.Session{
		UserID:   u.ID,
		DeviceID: device.ID,
		Epoch:    u.SessionEpoch,
	}, time.Now().UTC())
	if err != nil {
		return nil, problem.Error(ctx, err)
	}

	body := newUserResponse(u)

	return &CreateSessionOutput{
		Status: http.StatusCreated,
		Body: SessionResponse{
			DeviceToken: deviceToken,
			Token:       token,
			ExpiresAt:   &expires,
			User:        &body,
		},
	}, nil
}

func (h *sessionHandlers) deliverCode(ctx context.Context, email, code string) error {
	if h.mail == nil {
		return fmt.Errorf("v1: no mail sender configured for two-factor delivery")
	}

	if err := h.mail.SendTwoFactorCode(ctx, email, code); err != nil {
		logger(h.log).ErrorContext(ctx, "two-factor mail failed", "error", err)

		return err
	}

	return nil
}

// signInContext lifts the transport facts the middleware recorded into the
// domain's shape.
func signInContext(ctx context.Context, deviceToken string) user.SignInContext {
	client := reqctx.ClientFrom(ctx)

	return user.SignInContext{
		IP:          client.IP,
		UserAgent:   client.UserAgent,
		DeviceToken: deviceToken,
	}
}
