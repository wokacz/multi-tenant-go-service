package models_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/wokacz/go-example/internal/store/models"
)

func TestModelBeforeCreateAssignsOrderedUUID(t *testing.T) {
	var m models.Model
	if err := m.BeforeCreate(nil); err != nil {
		t.Fatalf("BeforeCreate() = %v, want nil", err)
	}

	if m.ID == uuid.Nil {
		t.Fatal("BeforeCreate() left the ID unset")
	}
	if got := m.ID.Version(); got != 7 {
		t.Errorf("ID version = %d, want 7 (time-ordered)", got)
	}
}

func TestModelBeforeCreateKeepsExistingID(t *testing.T) {
	want := uuid.New()
	m := models.Model{ID: want}

	if err := m.BeforeCreate(nil); err != nil {
		t.Fatalf("BeforeCreate() = %v, want nil", err)
	}
	if m.ID != want {
		t.Errorf("ID = %v, want %v (caller-supplied ID overwritten)", m.ID, want)
	}
}

func TestDeviceTrustLifecycle(t *testing.T) {
	var d models.Device

	if d.IsTrusted() {
		t.Fatal("a fresh device reports as trusted")
	}

	if err := d.Trust(); err != nil {
		t.Fatalf("Trust() = %v, want nil", err)
	}
	if !d.IsTrusted() {
		t.Error("device is not trusted after Trust()")
	}

	if err := d.Revoke(); err != nil {
		t.Fatalf("Revoke() = %v, want nil", err)
	}
	if d.IsTrusted() {
		t.Error("device is still trusted after Revoke()")
	}
	if !d.IsRevoked() {
		t.Error("device is not revoked after Revoke()")
	}
}

func TestDeviceRevokeIsIdempotentlyRejected(t *testing.T) {
	var d models.Device

	if err := d.Revoke(); err != nil {
		t.Fatalf("Revoke() = %v, want nil", err)
	}
	if err := d.Revoke(); !errors.Is(err, models.ErrDeviceRevoked) {
		t.Errorf("second Revoke() = %v, want ErrDeviceRevoked", err)
	}
	if err := d.Trust(); !errors.Is(err, models.ErrDeviceRevoked) {
		t.Errorf("Trust() on a revoked device = %v, want ErrDeviceRevoked", err)
	}
}

func TestDeviceUnrevokeRestoresTrustabilityNotTrust(t *testing.T) {
	var d models.Device
	if err := d.Trust(); err != nil {
		t.Fatalf("Trust() = %v, want nil", err)
	}
	if err := d.Revoke(); err != nil {
		t.Fatalf("Revoke() = %v, want nil", err)
	}

	d.Unrevoke()

	if d.IsRevoked() {
		t.Error("device is still revoked after Unrevoke()")
	}
	if d.IsTrusted() {
		t.Error("Unrevoke() silently restored trust; the user must re-confirm")
	}
	if err := d.Trust(); err != nil {
		t.Errorf("Trust() after Unrevoke() = %v, want nil", err)
	}
}

func TestSoftDeleteProtection(t *testing.T) {
	s := models.SoftDelete{IsProtected: true}

	if err := s.Delete(); !errors.Is(err, models.ErrProtected) {
		t.Errorf("Delete() = %v, want ErrProtected", err)
	}
	if err := s.BeforeDelete(nil); !errors.Is(err, models.ErrProtected) {
		t.Errorf("BeforeDelete() = %v, want ErrProtected", err)
	}
	if s.IsDeleted() {
		t.Error("a protected record was marked deleted")
	}
}

func TestSoftDeleteRoundTrip(t *testing.T) {
	var s models.SoftDelete

	if err := s.Delete(); err != nil {
		t.Fatalf("Delete() = %v, want nil", err)
	}
	if !s.IsDeleted() {
		t.Error("record is not marked deleted after Delete()")
	}

	s.Restore()

	if s.IsDeleted() {
		t.Error("record is still marked deleted after Restore()")
	}
	if err := s.BeforeDelete(nil); err != nil {
		t.Errorf("BeforeDelete() on an unprotected record = %v, want nil", err)
	}
}

func TestLoginOutcomeValid(t *testing.T) {
	valid := []models.LoginOutcome{
		models.OutcomeSuccess,
		models.OutcomeBadPassword,
		models.OutcomeMFAFailed,
		models.OutcomeLocked,
	}
	for _, o := range valid {
		if !o.Valid() {
			t.Errorf("LoginOutcome(%q).Valid() = false, want true", o)
		}
	}

	for _, o := range []models.LoginOutcome{"", "SUCCESS", "expired"} {
		if o.Valid() {
			t.Errorf("LoginOutcome(%q).Valid() = true, want false", o)
		}
	}
}

func TestLoginEventBeforeSaveRejectsUnknownOutcome(t *testing.T) {
	e := models.LoginEvent{Outcome: "definitely_not_an_outcome"}
	if err := e.BeforeSave(nil); err == nil {
		t.Error("BeforeSave() = nil, want an error for an unknown outcome")
	}

	e.Outcome = models.OutcomeSuccess
	if err := e.BeforeSave(nil); err != nil {
		t.Errorf("BeforeSave() = %v, want nil", err)
	}
}
