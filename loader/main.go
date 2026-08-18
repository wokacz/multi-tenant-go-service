// Command loader prints the DDL implied by the GORM models, for Atlas to diff
// against the migration directory. Atlas runs it; it is not part of the API.
//
// It is a module of its own (see go.mod beside this file) because
// atlas-provider-gorm depends on every GORM driver — MySQL, SQL Server, the lot
// — and none of that belongs in the dependency graph of a service that talks
// only to Postgres.
package main

import (
	"fmt"
	"io"
	"os"

	"ariga.io/atlas-provider-gorm/gormschema"

	"github.com/wokacz/multi-tenant-go-service/internal/store/models"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "loader:", err)
		os.Exit(1)
	}
}

func run() error {
	// Every model has to be listed. One left out is silently absent from the
	// generated schema, and Atlas would then propose dropping its table.
	stmts, err := gormschema.New("postgres").Load(
		&models.User{},
		&models.Device{},
		&models.LoginEvent{},
		&models.PasswordReset{},
		&models.EmailChange{},
		&models.TwoFactorChallenge{},
		&models.Organization{},
		&models.Membership{},
		&models.Invitation{},
		&models.InvitationRole{},
		&models.Role{},
		&models.RolePermission{},
		&models.MembershipRole{},
		&models.UserSystemRole{},
		&models.AuthzEvent{},
	)
	if err != nil {
		return fmt.Errorf("load gorm schema: %w", err)
	}

	_, err = io.WriteString(os.Stdout, stmts)

	return err
}
