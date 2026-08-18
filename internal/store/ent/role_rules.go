package ent

import (
	"fmt"
	"unicode/utf8"

	"github.com/google/uuid"
)

const MaxRoleKeyLength = 64

func (r *Role) Validate() error {
	if !validRoleKey(r.Key) {
		return fmt.Errorf("ent: invalid role key %q", r.Key)
	}

	if r.Name == "" || utf8.RuneCountInString(r.Name) > 100 {
		return fmt.Errorf("ent: invalid role name %q", r.Name)
	}

	return nil
}

func (r *Role) RefuseDelete() error {
	if r.ID == uuid.Nil {
		return ErrRoleBatchDeleteUnsupported
	}

	if r.IsSystem {
		return ErrRoleIsSystem
	}

	return nil
}

func validRoleKey(key string) bool {
	if key == "" || len(key) > MaxRoleKeyLength {
		return false
	}

	if key[0] == '_' || key[len(key)-1] == '_' {
		return false
	}

	for i := range len(key) {
		c := key[i]

		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '_':
			if key[i-1] == '_' {
				return false
			}
		default:
			return false
		}
	}

	return true
}

func (r *UserSystemRole) Validate() error {
	if !validRoleKey(r.RoleKey) {
		return fmt.Errorf("ent: invalid system role key %q", r.RoleKey)
	}

	return nil
}
