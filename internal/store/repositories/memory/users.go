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

	"github.com/wokacz/go-example/internal/domain/user"
	"github.com/wokacz/go-example/internal/store/models"
)

// Users is an in-memory user.Repository. The zero value is not usable; call
// NewUsers.
type Users struct {
	mu sync.Mutex

	users      map[uuid.UUID]*models.User
	byEmail    map[string]uuid.UUID
	resets     map[uuid.UUID]*models.PasswordReset
	devices    map[uuid.UUID]*models.Device
	challenges map[uuid.UUID]*models.TwoFactorChallenge
	events     []models.LoginEvent
}

// Compile-time check, the same one the GORM implementation carries.
var _ user.Repository = (*Users)(nil)

func NewUsers() *Users {
	return &Users{
		users:      map[uuid.UUID]*models.User{},
		byEmail:    map[string]uuid.UUID{},
		resets:     map[uuid.UUID]*models.PasswordReset{},
		devices:    map[uuid.UUID]*models.Device{},
		challenges: map[uuid.UUID]*models.TwoFactorChallenge{},
	}
}

func (m *Users) Create(_ context.Context, u *models.User) error {
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

func (m *Users) ByID(_ context.Context, id uuid.UUID) (*models.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	u, ok := m.users[id]
	if !ok {
		return nil, user.ErrNotFound
	}

	return copyOf(u), nil
}

func (m *Users) ByEmail(_ context.Context, email string) (*models.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id, ok := m.byEmail[email]
	if !ok {
		return nil, user.ErrNotFound
	}

	return copyOf(m.users[id]), nil
}

func (m *Users) ReplacePasswordReset(_ context.Context, reset *models.PasswordReset) error {
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

func (m *Users) ActivePasswordReset(_ context.Context, userID uuid.UUID, now time.Time) (*models.PasswordReset, error) {
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

func (m *Users) ConsumePasswordReset(_ context.Context, reset *models.PasswordReset, passwordHash string) error {
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

func (m *Users) DeviceByFingerprint(_ context.Context, userID uuid.UUID, fingerprint string) (*models.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, device := range m.devices {
		if device.UserID == userID && device.Fingerprint == fingerprint {
			return copyOf(device), nil
		}
	}

	return nil, user.ErrNotFound
}

func (m *Users) ActiveDevice(_ context.Context, userID, deviceID uuid.UUID) (*models.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	device, ok := m.devices[deviceID]
	if !ok || device.UserID != userID || device.IsRevoked() {
		return nil, user.ErrNotFound
	}

	return copyOf(device), nil
}

func (m *Users) CreateDevice(_ context.Context, device *models.Device) error {
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

	// Revoking twice succeeds, matching the GORM implementation.
	_ = device.Revoke()

	return nil
}

func (m *Users) Devices(_ context.Context, userID uuid.UUID) ([]models.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []models.Device

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

func (m *Users) RecordLoginEvent(_ context.Context, event *models.LoginEvent) error {
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

func (m *Users) LoginEvents(_ context.Context, userID uuid.UUID, limit int) ([]models.LoginEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []models.LoginEvent

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

func (m *Users) ReplaceTwoFactorChallenge(_ context.Context, challenge *models.TwoFactorChallenge) error {
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

func (m *Users) ActiveTwoFactorChallenge(_ context.Context, userID uuid.UUID, now time.Time) (*models.TwoFactorChallenge, error) {
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

func lastSeen(d models.Device) time.Time {
	if d.LastSeenAt == nil {
		return time.Time{}
	}

	return *d.LastSeenAt
}

func copyOf[T any](v *T) *T {
	out := *v

	return &out
}
