package ent

import (
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/loginevent"
)

func (e *LoginEvent) Validate() error {
	return loginevent.OutcomeValidator(e.Outcome)
}
