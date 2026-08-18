package httptest

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/authz"
)

type inviteResults struct {
	Results []struct {
		Email  string `json:"email"`
		Status string `json:"status"`
	} `json:"results"`
}

func (r inviteResults) statusOf(email string) string {
	for _, result := range r.Results {
		if result.Email == email {
			return result.Status
		}
	}

	return "<missing>"
}

// TestABatchInvitesEveryoneAndMailsEachOne is the case that made this exist:
// onboarding a team used to be one Request per person, each identical apart from
// the address, and the sixth one hit the rate limit.
func TestABatchInvitesEveryoneAndMailsEachOne(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleOwner)

	var got inviteResults
	f.call(t, http.MethodPost, f.orgPath("/invitations"),
		`{"emails":["bo@example.com","cy@example.com","dana@example.com"],"role_ids":[]}`).
		expect(t, http.StatusOK).decode(t, &got)

	if len(got.Results) != 3 {
		t.Fatalf("%d results, want 3", len(got.Results))
	}

	for _, email := range []string{"bo@example.com", "cy@example.com", "dana@example.com"} {
		if status := got.statusOf(email); status != "invited" {
			t.Errorf("%s = %q, want invited", email, status)
		}
	}

	// One message per invitation, each carrying its own token. A batch that mailed
	// once, or mailed the same token twice, would let one invitee accept as another.
	tokens := map[string]string{}

	for _, sent := range f.Mailer.Invitations {
		tokens[sent.Token] = sent.Email
	}

	if len(tokens) != 3 {
		t.Errorf("%d distinct tokens for 3 invitations: %v", len(tokens), tokens)
	}
}

// TestOneAlreadyMemberDoesNotSinkTheBatch is why the response is a list of outcomes
// rather than a status code.
//
// An administrator pasting twelve colleagues, two of whom are already in, wants the
// ten invitations sent. All-or-nothing would make them find the two by bisection.
func TestOneAlreadyMemberDoesNotSinkTheBatch(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleOwner)

	var got inviteResults
	f.call(t, http.MethodPost, f.orgPath("/invitations"),
		`{"emails":["bo@example.com","`+TestEmail+`"],"role_ids":[]}`).
		expect(t, http.StatusOK).decode(t, &got)

	if status := got.statusOf("bo@example.com"); status != "invited" {
		t.Errorf("bo = %q, want invited despite the other address failing", status)
	}

	if status := got.statusOf(TestEmail); status != "already_member" {
		t.Errorf("the caller's own address = %q, want already_member", status)
	}
}

// TestABatchCannotGrantMoreThanTheCallerHolds keeps the anti-escalation rule on the
// batch. It is checked once, before anything is written, because the role set is the
// same for every address — fifty identical refusals is not a better answer.
func TestABatchCannotGrantMoreThanTheCallerHolds(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleAdmin)

	owner := f.Repo.SeedShippedRole(f.OrgID, authz.RoleOwner)

	var doc ProblemBody
	f.call(t, http.MethodPost, f.orgPath("/invitations"),
		`{"emails":["bo@example.com"],"role_ids":["`+owner.String()+`"]}`).
		expect(t, http.StatusForbidden).decode(t, &doc)

	if doc.Code != "privilege_escalation" {
		t.Errorf("code = %q, want privilege_escalation", doc.Code)
	}

	// Nothing was written, and nothing was mailed.
	if len(f.Mailer.Invitations) != 0 {
		t.Errorf("%d invitations went out after a refused batch", len(f.Mailer.Invitations))
	}
}

// TestABatchRefusesTheSameAddressTwice covers the check the schema cannot make.
// uniqueItems compares the strings it was sent; only normalising knows that
// Ada@example.com and ada@example.com are one address, and inviting one address
// twice in one Request would produce an invitation and then a refusal for it.
func TestABatchRefusesTheSameAddressTwice(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleOwner)

	var doc ProblemBody
	f.call(t, http.MethodPost, f.orgPath("/invitations"),
		`{"emails":["Bo@example.com","bo@example.com"],"role_ids":[]}`).
		expect(t, http.StatusUnprocessableEntity).decode(t, &doc)

	if doc.Code != "invalid_invitation_batch" {
		t.Errorf("code = %q, want invalid_invitation_batch", doc.Code)
	}

	if len(f.Mailer.Invitations) != 0 {
		t.Error("a refused batch still mailed somebody")
	}
}

// TestTheBatchIsCapped pins the schema half: the cap is in the contract, so a
// client learns it from the document rather than from a 422 in production.
func TestTheBatchIsCapped(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleOwner)

	addresses := make([]string, 0, 51)
	for i := range 51 {
		addresses = append(addresses, `"`+string(rune('a'+i%26))+uuid.Must(uuid.NewV7()).String()+`@example.com"`)
	}

	f.call(t, http.MethodPost, f.orgPath("/invitations"),
		`{"emails":[`+strings.Join(addresses, ",")+`],"role_ids":[]}`).
		expect(t, http.StatusUnprocessableEntity)

	if len(f.Mailer.Invitations) != 0 {
		t.Error("an over-long batch still mailed somebody")
	}
}

// TestAnInvitedAddressBecomesAMemberByAccepting closes the loop: a batch produces
// the same rows as inviting one at a time, so the token from the message still
// works.
func TestAnInvitedAddressBecomesAMemberByAccepting(t *testing.T) {
	f := NewAuthzFixture(t, authz.RoleOwner)

	const invitee = "bo@example.com"

	f.call(t, http.MethodPost, f.orgPath("/invitations"),
		`{"emails":["`+invitee+`"],"role_ids":[]}`).
		expect(t, http.StatusOK)

	if len(f.Mailer.Invitations) != 1 {
		t.Fatalf("%d messages, want 1", len(f.Mailer.Invitations))
	}

	token := f.Mailer.Invitations[0].Token

	registerAccount(t, f, invitee)
	bo := signIn(t, f.Server, invitee, TestPassword)

	if rec := Do(t, f.Server.Handler(), Authed(t, http.MethodPost,
		"/v1/me/invitations/accept", `{"token":"`+token+`"}`, bo, "")); rec.Code != http.StatusNoContent {
		t.Fatalf("accept = %d; body %s", rec.Code, rec.Body.Bytes())
	}

	var members struct {
		Members []struct {
			Email string `json:"email"`
		} `json:"members"`
	}
	f.call(t, http.MethodGet, f.orgPath("/members"), "").
		expect(t, http.StatusOK).decode(t, &members)

	found := false

	for _, m := range members.Members {
		if m.Email == invitee {
			found = true
		}
	}

	if !found {
		t.Error("the invitee accepted but is not a member")
	}
}
