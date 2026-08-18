package ent

import (
	"fmt"
	"strings"
	"time"
)

func (i *Invitation) Validate() error {
	if strings.TrimSpace(i.Email) == "" {
		return fmt.Errorf("ent: invitation email is empty")
	}

	if strings.TrimSpace(i.TokenHash) == "" {
		return fmt.Errorf("ent: invitation token hash is empty")
	}

	return nil
}

func (i Invitation) Pending(now time.Time) bool {
	return i.AcceptedAt == nil && i.ExpiresAt.After(now)
}
