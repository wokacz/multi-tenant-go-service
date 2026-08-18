package seed

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wokacz/multi-tenant-go-service/internal/config"
	"github.com/wokacz/multi-tenant-go-service/internal/domain/user"
)

// Three refusals, and they are not the same kind of thing.
//
// ErrProduction cannot be overridden by any flag. The other two can, because they
// protect against a mistake rather than against catastrophe, and a guard with no way
// past it gets worked around by copying the code somewhere else.
var (
	// ErrProduction is the one that matters. Nothing here is safe to run against
	// real accounts: it writes a known password onto every account it creates and
	// deletes one on purpose.
	ErrProduction = errors.New("seed: refusing to run with ENV=production")

	// ErrNotConfirmed is the accidental-invocation guard. `go run ./cmd/seed` in the
	// wrong terminal should do nothing at all.
	ErrNotConfirmed = errors.New("seed: pass -yes to confirm")

	// ErrForeignData is the dev-database guard: somebody's own test accounts are
	// data they made and would have to make again.
	ErrForeignData = errors.New("seed: the database has accounts this seeder did not create")
)

// foreignScanLimit bounds the check below.
//
// The seeder deliberately does not get its own repository method for "count
// accounts outside a domain": it is a development tool, and widening the domain
// interface for it would put a query nothing else needs in front of everybody. So it
// pages through the listing that already exists and stops after this many. On a
// database large enough for the cap to matter, the first page has already answered
// the question.
const foreignScanLimit = 500

// Guard decides whether seeding may proceed.
//
// Layers, in order of how much they protect:
//
//  1. the environment, which no flag can override;
//  2. an explicit confirmation, so nothing happens by accident;
//  3. whether this database already holds data somebody else made.
//
// force skips the third only. It exists for the case where a developer knows their
// own account is in there and wants the seed data anyway.
func Guard(ctx context.Context, cfg *config.Config, users user.Repository, confirmed, force bool) error {
	if cfg.Env.IsProduction() {
		return ErrProduction
	}

	if !confirmed {
		return ErrNotConfirmed
	}

	if force {
		return nil
	}

	foreign, err := foreignAccount(ctx, users)
	if err != nil {
		return err
	}

	if foreign != "" {
		return fmt.Errorf("%w: %s is not a %s address; pass -force to seed anyway",
			ErrForeignData, foreign, Domain)
	}

	return nil
}

// foreignAccount returns the first address that is not the seeder's own, or "".
//
// Reserved-domain addresses are what makes this possible: .test can never be a real
// mailbox (RFC 6761), so "the address ends in seed.test" is a reliable statement that
// this row came from here and not from a person.
func foreignAccount(ctx context.Context, users user.Repository) (string, error) {
	suffix := "@" + Domain

	for offset := 0; offset < foreignScanLimit; offset += user.MaxUserPage {
		page, err := users.All(ctx, user.MaxUserPage, offset)
		if err != nil {
			return "", err
		}

		for i := range page {
			if !strings.HasSuffix(page[i].Email, suffix) {
				return page[i].Email, nil
			}
		}

		if len(page) < user.MaxUserPage {
			break
		}
	}

	return "", nil
}
