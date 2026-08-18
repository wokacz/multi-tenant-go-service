package ent

import "errors"

var (
	ErrProtected = errors.New("ent: record is protected from deletion")

	ErrBatchDeleteUnsupported = errors.New("ent: deleting a user requires a primary key so its devices can be revoked")

	ErrDeviceRevoked = errors.New("ent: device is revoked")

	ErrRoleIsSystem = errors.New("ent: system roles cannot be modified or deleted")

	ErrRoleBatchDeleteUnsupported = errors.New("ent: deleting a role requires a primary key so system roles stay protected")
)
