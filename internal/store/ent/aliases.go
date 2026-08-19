package ent

import (
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/file"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/loginevent"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/membership"
)

// Names the generated enums under this package so callers do not have to import
// the per-table packages for two constants.
const (
	MembershipActive    = membership.StatusActive
	MembershipSuspended = membership.StatusSuspended

	OutcomeSuccess     = loginevent.OutcomeSuccess
	OutcomeBadPassword = loginevent.OutcomeBadPassword
	OutcomeMFAFailed   = loginevent.OutcomeMfaFailed
	OutcomeLocked      = loginevent.OutcomeLocked

	FileScanSkipped     = file.ScanStatusSkipped
	FileScanClean       = file.ScanStatusClean
	FileScanUnavailable = file.ScanStatusUnavailable
)

type (
	MembershipStatus = membership.Status
	LoginOutcome     = loginevent.Outcome
	FileScanStatus   = file.ScanStatus
)
