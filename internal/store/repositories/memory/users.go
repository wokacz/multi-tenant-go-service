// Package memory is a user.Repository that keeps everything in maps.
//
// It exists so the API tests and the domain tests exercise the same fake
// instead of each maintaining a stub of their own. Two hand-written stubs of a
// twenty-method interface drift, and when they drift the suite that has the
// laxer one stops testing anything: a rule the real store enforces can be
// broken in a handler and only the other package's tests would notice.
//
// The semantics that matter are copied deliberately, not approximated:
// FailPasswordReset and FailTwoFactorChallenge move their counters under the
// same lock that reads them, and reads hand back copies, so a caller cannot
// mutate stored state by holding on to a returned pointer. Tests that depend
// on either property would otherwise pass here and fail against Postgres.
package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent"
)

// Users is an in-memory user.Repository. The zero value is not usable; call
// NewUsers.
type Users struct {
	mu sync.Mutex

	users      map[uuid.UUID]*ent.User
	byEmail    map[string]uuid.UUID
	resets     map[uuid.UUID]*ent.PasswordReset
	emailChg   map[uuid.UUID]*ent.EmailChange
	devices    map[uuid.UUID]*ent.Device
	challenges map[uuid.UUID]*ent.TwoFactorChallenge
	events     []ent.LoginEvent
}

// Compile-time check that this still satisfies the interface the domain declares.
var _ user.Repository = (*Users)(nil)

func NewUsers() *Users {
	return &Users{
		users:      map[uuid.UUID]*ent.User{},
		byEmail:    map[string]uuid.UUID{},
		resets:     map[uuid.UUID]*ent.PasswordReset{},
		emailChg:   map[uuid.UUID]*ent.EmailChange{},
		devices:    map[uuid.UUID]*ent.Device{},
		challenges: map[uuid.UUID]*ent.TwoFactorChallenge{},
	}
}

func (m *Users) Create(_ context.Context, u *ent.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.byEmail[u.Email]; ok {
		return user.ErrEmailTaken
	}

	if u.ID == uuid.Nil {
		u.ID = uuid.Must(uuid.NewV7())
	}

	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC()
	}

	stored := *u
	m.users[u.ID] = &stored
	m.byEmail[u.Email] = u.ID

	return nil
}

// ByID hides a deleted account, which is what the SQL interceptor does without
// anybody writing the predicate at each call site. It matters more than it
// looks: requireBearer loads the account on every request, so this is what makes a
// deleted account's token stop working.
func (m *Users) ByID(_ context.Context, id uuid.UUID) (*ent.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	u, ok := m.users[id]
	if !ok || u.IsDeleted() {
		return nil, user.ErrNotFound
	}

	return copyOf(u), nil
}

func (m *Users) All(_ context.Context, limit, offset int) ([]ent.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]ent.User, 0, len(m.users))
	for _, u := range m.users {
		if !u.IsDeleted() {
			out = append(out, *u)
		}
	}

	// The SQL orders by id descending; map iteration does not, and a test
	// asserting on the first element would otherwise pass at random.
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID.String() > out[j].ID.String()
	})

	if offset >= len(out) {
		return nil, nil
	}

	out = out[offset:]
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}

	return out, nil
}

func (m *Users) Delete(_ context.Context, userID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	u, ok := m.users[userID]
	if !ok {
		return user.ErrNotFound
	}

	if err := u.Delete(); err != nil {
		return err
	}

	// The address is free again. byEmail is this fake's unique index, and the SQL
	// one is partial — unique only among live accounts — so a deleted account must
	// stop occupying its address here too. Without this the fake would keep
	// refusing a registration Postgres accepts.
	delete(m.byEmail, u.Email)

	// Soft delete does not cascade, so the fake revokes the devices here the
	// same way the Postgres repository does before it retires the account.
	for _, device := range m.devices {
		if device.UserID == userID && !device.IsRevoked() {
			_ = device.Revoke()
		}
	}

	return nil
}

func (m *Users) UpdateProfile(_ context.Context, userID uuid.UUID, name, locale string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	u, ok := m.users[userID]
	if !ok || u.IsDeleted() {
		return user.ErrNotFound
	}

	u.Name = name
	u.Locale = locale

	return nil
}

func (m *Users) SetPassword(_ context.Context, userID uuid.UUID, passwordHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	u, ok := m.users[userID]
	if !ok || u.IsDeleted() {
		return user.ErrNotFound
	}

	u.PasswordHash = passwordHash
	u.SessionEpoch++

	return nil
}

func (m *Users) BumpSessionEpoch(_ context.Context, userID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	u, ok := m.users[userID]
	if !ok || u.IsDeleted() {
		return user.ErrNotFound
	}

	u.SessionEpoch++

	return nil
}

// IsSoftDeleted reports whether the fake holds a deleted row for that id.
//
// It exists for the other fake. Authz has to answer "is this membership's account
// gone", the two live in separate objects here, and in Postgres they are one
// database — so without this, deleting an account through this repository left Authz
// still counting it as a member. The seeder found that, because a seeder can only
// use the real interfaces and cannot reach for a fixture to paper over it.
//
// An id this fake has never seen is not deleted: a membership with no account row is
// unrepresentable in Postgres (the foreign key forbids it) but common in tests, and
// treating those as deleted would change what every one of them means.
func (m *Users) IsSoftDeleted(id uuid.UUID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	u, ok := m.users[id]

	return ok && u.IsDeleted()
}

func (m *Users) SetSuspended(_ context.Context, userID uuid.UUID, at *time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	u, ok := m.users[userID]
	if !ok {
		return user.ErrNotFound
	}

	u.SuspendedAt = at
	if at != nil {
		// Same statement as the SQL: suspending invalidates tokens already out.
		u.SessionEpoch++
	}

	return nil
}

func (m *Users) ByEmail(_ context.Context, email string) (*ent.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id, ok := m.byEmail[email]
	if !ok {
		return nil, user.ErrNotFound
	}

	if u := m.users[id]; u == nil || u.IsDeleted() {
		return nil, user.ErrNotFound
	}

	return copyOf(m.users[id]), nil
}

func (m *Users) ReplacePasswordReset(_ context.Context, reset *ent.PasswordReset) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, existing := range m.resets {
		if existing.UserID == reset.UserID && existing.ConsumedAt == nil {
			delete(m.resets, id)
		}
	}

	if reset.ID == uuid.Nil {
		reset.ID = uuid.Must(uuid.NewV7())
	}

	stored := *reset
	m.resets[reset.ID] = &stored

	return nil
}

func (m *Users) ActivePasswordReset(_ context.Context, userID uuid.UUID, now time.Time) (*ent.PasswordReset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, reset := range m.resets {
		if reset.UserID == userID && reset.ConsumedAt == nil && reset.ExpiresAt.After(now) {
			return copyOf(reset), nil
		}
	}

	return nil, user.ErrNotFound
}

func (m *Users) FailPasswordReset(_ context.Context, resetID uuid.UUID, maxAttempts int, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	reset, ok := m.resets[resetID]
	if !ok || reset.ConsumedAt != nil {
		return nil
	}

	reset.Attempts++
	if reset.Attempts >= maxAttempts {
		spent := now
		reset.ConsumedAt = &spent
	}

	return nil
}

func (m *Users) ConsumePasswordReset(_ context.Context, reset *ent.PasswordReset, passwordHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	u, ok := m.users[reset.UserID]
	if !ok {
		return user.ErrNotFound
	}

	u.PasswordHash = passwordHash
	u.SessionEpoch++

	if stored, ok := m.resets[reset.ID]; ok {
		stored.ConsumedAt = reset.ConsumedAt
		stored.Attempts = reset.Attempts
	}

	return nil
}

// The four email-change methods mirror the reset ones exactly, including the
// attempt counter moving under the mutex rather than being read and written back.

func (m *Users) ReplaceEmailChange(_ context.Context, change *ent.EmailChange) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, existing := range m.emailChg {
		if existing.UserID == change.UserID && existing.ConsumedAt == nil {
			delete(m.emailChg, id)
		}
	}

	if change.ID == uuid.Nil {
		change.ID = uuid.Must(uuid.NewV7())
	}

	stored := *change
	m.emailChg[change.ID] = &stored

	return nil
}

func (m *Users) ActiveEmailChange(_ context.Context, userID uuid.UUID, now time.Time) (*ent.EmailChange, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, change := range m.emailChg {
		if change.UserID == userID && change.ConsumedAt == nil && change.ExpiresAt.After(now) {
			copied := *change

			return &copied, nil
		}
	}

	return nil, user.ErrNotFound
}

func (m *Users) FailEmailChange(_ context.Context, changeID uuid.UUID, maxAttempts int, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	change, ok := m.emailChg[changeID]
	if !ok || change.ConsumedAt != nil {
		return nil
	}

	change.Attempts++
	if change.Attempts >= maxAttempts {
		spent := now
		change.ConsumedAt = &spent
	}

	return nil
}

func (m *Users) ConsumeEmailChange(_ context.Context, change *ent.EmailChange, email string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	u, ok := m.users[change.UserID]
	if !ok {
		return user.ErrNotFound
	}

	// byEmail is this fake's unique index, so the answer about a taken address
	// comes from the same place Postgres gets it: the constraint, not a lookup the
	// caller could have made earlier.
	if owner, taken := m.byEmail[email]; taken && owner != u.ID {
		return user.ErrEmailTaken
	}

	delete(m.byEmail, u.Email)
	u.Email = email
	m.byEmail[email] = u.ID

	if stored, ok := m.emailChg[change.ID]; ok {
		stored.ConsumedAt = change.ConsumedAt
		stored.Attempts = change.Attempts
	}

	return nil
}

func (m *Users) DeviceByFingerprint(_ context.Context, userID uuid.UUID, fingerprint string) (*ent.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, device := range m.devices {
		if device.UserID == userID && device.Fingerprint == fingerprint {
			return copyOf(device), nil
		}
	}

	return nil, user.ErrNotFound
}

func (m *Users) ActiveDevice(_ context.Context, userID, deviceID uuid.UUID) (*ent.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	device, ok := m.devices[deviceID]
	if !ok || device.UserID != userID || device.IsRevoked() {
		return nil, user.ErrNotFound
	}

	return copyOf(device), nil
}

func (m *Users) CreateDevice(_ context.Context, device *ent.Device) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if device.ID == uuid.Nil {
		device.ID = uuid.Must(uuid.NewV7())
	}

	if device.CreatedAt.IsZero() {
		device.CreatedAt = time.Now().UTC()
	}

	stored := *device
	m.devices[device.ID] = &stored

	return nil
}

func (m *Users) TouchDevice(_ context.Context, deviceID uuid.UUID, seenAt time.Time, ip, userAgent string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	device, ok := m.devices[deviceID]
	if !ok {
		return user.ErrNotFound
	}

	at, address := seenAt, ip
	device.LastSeenAt = &at
	device.LastIP = &address
	device.UserAgent = userAgent

	return nil
}

func (m *Users) TrustDevice(_ context.Context, deviceID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	device, ok := m.devices[deviceID]
	if !ok {
		return user.ErrNotFound
	}

	if err := device.Trust(); err != nil {
		return user.ErrDeviceRevoked
	}

	return nil
}

func (m *Users) RevokeDevice(_ context.Context, userID, deviceID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	device, ok := m.devices[deviceID]
	if !ok || device.UserID != userID {
		return user.ErrNotFound
	}

	// Revoking twice succeeds: the device is already revoked.
	_ = device.Revoke()

	return nil
}

func (m *Users) Devices(_ context.Context, userID uuid.UUID) ([]ent.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []ent.Device

	for _, device := range m.devices {
		if device.UserID == userID {
			out = append(out, *device)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		return lastSeen(out[i]).After(lastSeen(out[j]))
	})

	return out, nil
}

func (m *Users) RecordLoginEvent(_ context.Context, event *ent.LoginEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if event.ID == uuid.Nil {
		event.ID = uuid.Must(uuid.NewV7())
	}

	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}

	m.events = append(m.events, *event)

	return nil
}

func (m *Users) LoginEvents(_ context.Context, userID uuid.UUID, limit int) ([]ent.LoginEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []ent.LoginEvent

	// Newest first, which for equal timestamps means insertion order reversed
	// — UUIDv7 ids are time-ordered, so this matches what the index would give.
	for i := len(m.events) - 1; i >= 0; i-- {
		if m.events[i].UserID != userID {
			continue
		}

		out = append(out, m.events[i])
		if len(out) == limit {
			break
		}
	}

	return out, nil
}

func (m *Users) SetTwoFactorEnabled(_ context.Context, userID uuid.UUID, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	u, ok := m.users[userID]
	if !ok {
		return user.ErrNotFound
	}

	u.TwoFactorEnabled = enabled

	return nil
}

func (m *Users) ReplaceTwoFactorChallenge(_ context.Context, challenge *ent.TwoFactorChallenge) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, existing := range m.challenges {
		if existing.UserID == challenge.UserID && existing.ConsumedAt == nil {
			delete(m.challenges, id)
		}
	}

	if challenge.ID == uuid.Nil {
		challenge.ID = uuid.Must(uuid.NewV7())
	}

	stored := *challenge
	m.challenges[challenge.ID] = &stored

	return nil
}

func (m *Users) ActiveTwoFactorChallenge(_ context.Context, userID uuid.UUID, now time.Time) (*ent.TwoFactorChallenge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, challenge := range m.challenges {
		if challenge.UserID == userID && challenge.ConsumedAt == nil && challenge.ExpiresAt.After(now) {
			return copyOf(challenge), nil
		}
	}

	return nil, user.ErrNotFound
}

func (m *Users) FailTwoFactorChallenge(_ context.Context, challengeID uuid.UUID, maxAttempts int, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	challenge, ok := m.challenges[challengeID]
	if !ok || challenge.ConsumedAt != nil {
		return nil
	}

	challenge.Attempts++
	if challenge.Attempts >= maxAttempts {
		spent := now
		challenge.ConsumedAt = &spent
	}

	return nil
}

func (m *Users) ConsumeTwoFactorChallenge(_ context.Context, challengeID, deviceID uuid.UUID, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	challenge, ok := m.challenges[challengeID]
	if !ok || challenge.ConsumedAt != nil {
		return user.ErrInvalidTwoFactorCode
	}

	device, ok := m.devices[deviceID]
	if !ok {
		return user.ErrNotFound
	}

	if err := device.Trust(); err != nil {
		return user.ErrDeviceRevoked
	}

	spent := at
	challenge.ConsumedAt = &spent

	return nil
}

// Attempts reports the recorded guesses against a user's live reset code. It is
// here so a test can assert that the cap actually counts without reaching into
// the maps.
func (m *Users) Attempts(userID uuid.UUID) (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, reset := range m.resets {
		if reset.UserID == userID {
			return reset.Attempts, reset.ConsumedAt == nil
		}
	}

	return 0, false
}

func lastSeen(d ent.Device) time.Time {
	if d.LastSeenAt == nil {
		return time.Time{}
	}

	return *d.LastSeenAt
}

func copyOf[T any](v *T) *T {
	out := *v

	return &out
}
