package schema

import (
	"context"
	"fmt"
	"unicode/utf8"

	"entgo.io/ent"

	gen "github.com/wokacz/multi-tenant-go-service/internal/store/ent"
	"github.com/wokacz/multi-tenant-go-service/internal/store/ent/hook"
)

// The hooks below call Validate on the generated types so the rules live in one
// place: the tests next to those methods, and the write path that reaches
// Postgres. A validator that only exists on the struct is a comment; one that
// only exists in a hook is a rule the in-memory fake cannot share.
//
// Updates that touch a subset of fields cannot reuse Validate() wholesale — that
// method requires the whole row, and a rename would then fail as "invalid slug".
// Those paths check the field that actually moved.

func (Organization) Hooks() []ent.Hook {
	return []ent.Hook{
		hook.On(
			func(next ent.Mutator) ent.Mutator {
				return hook.OrganizationFunc(func(ctx context.Context, m *gen.OrganizationMutation) (ent.Value, error) {
					if m.Op().Is(ent.OpCreate) {
						org := gen.Organization{}
						if slug, ok := m.Slug(); ok {
							org.Slug = slug
						}

						if name, ok := m.Name(); ok {
							org.Name = name
						}

						if err := org.Validate(); err != nil {
							return nil, err
						}
					} else if name, ok := m.Name(); ok {
						if name == "" || utf8.RuneCountInString(name) > 100 {
							return nil, fmt.Errorf("models: invalid organization name %q", name)
						}
					}

					return next.Mutate(ctx, m)
				})
			},
			ent.OpCreate|ent.OpUpdate|ent.OpUpdateOne,
		),
	}
}

func (Role) Hooks() []ent.Hook {
	return []ent.Hook{
		hook.On(
			func(next ent.Mutator) ent.Mutator {
				return hook.RoleFunc(func(ctx context.Context, m *gen.RoleMutation) (ent.Value, error) {
					if m.Op().Is(ent.OpCreate) {
						role := gen.Role{}
						if key, ok := m.Key(); ok {
							role.Key = key
						}

						if name, ok := m.Name(); ok {
							role.Name = name
						}

						if err := role.Validate(); err != nil {
							return nil, err
						}
					} else if name, ok := m.Name(); ok {
						if name == "" || utf8.RuneCountInString(name) > 100 {
							return nil, fmt.Errorf("models: invalid role name %q", name)
						}
					}

					return next.Mutate(ctx, m)
				})
			},
			ent.OpCreate|ent.OpUpdate|ent.OpUpdateOne,
		),
	}
}

func (Membership) Hooks() []ent.Hook {
	return []ent.Hook{
		hook.On(
			func(next ent.Mutator) ent.Mutator {
				return hook.MembershipFunc(func(ctx context.Context, m *gen.MembershipMutation) (ent.Value, error) {
					if !m.Op().Is(ent.OpCreate) {
						return next.Mutate(ctx, m)
					}

					row := gen.Membership{}
					if userID, ok := m.UserID(); ok {
						row.UserID = userID
					}

					if status, ok := m.Status(); ok {
						row.Status = status
					}

					if err := row.Validate(); err != nil {
						return nil, err
					}

					return next.Mutate(ctx, m)
				})
			},
			ent.OpCreate,
		),
	}
}

func (Invitation) Hooks() []ent.Hook {
	return []ent.Hook{
		hook.On(
			func(next ent.Mutator) ent.Mutator {
				return hook.InvitationFunc(func(ctx context.Context, m *gen.InvitationMutation) (ent.Value, error) {
					if !m.Op().Is(ent.OpCreate) {
						return next.Mutate(ctx, m)
					}

					inv := gen.Invitation{}
					if email, ok := m.Email(); ok {
						inv.Email = email
					}

					if hash, ok := m.TokenHash(); ok {
						inv.TokenHash = hash
					}

					if err := inv.Validate(); err != nil {
						return nil, err
					}

					return next.Mutate(ctx, m)
				})
			},
			ent.OpCreate,
		),
	}
}

func (AuthzEvent) Hooks() []ent.Hook {
	return []ent.Hook{
		hook.On(
			func(next ent.Mutator) ent.Mutator {
				return hook.AuthzEventFunc(func(ctx context.Context, m *gen.AuthzEventMutation) (ent.Value, error) {
					event := gen.AuthzEvent{}
					if action, ok := m.Action(); ok {
						event.Action = action
					}

					if actor, ok := m.ActorID(); ok {
						event.ActorID = actor
					}

					if err := event.Validate(); err != nil {
						return nil, err
					}

					return next.Mutate(ctx, m)
				})
			},
			ent.OpCreate,
		),
	}
}

func (UserSystemRole) Hooks() []ent.Hook {
	return []ent.Hook{
		hook.On(
			func(next ent.Mutator) ent.Mutator {
				return hook.UserSystemRoleFunc(func(ctx context.Context, m *gen.UserSystemRoleMutation) (ent.Value, error) {
					row := gen.UserSystemRole{}
					if key, ok := m.RoleKey(); ok {
						row.RoleKey = key
					}

					if err := row.Validate(); err != nil {
						return nil, err
					}

					return next.Mutate(ctx, m)
				})
			},
			ent.OpCreate,
		),
	}
}

func (LoginEvent) Hooks() []ent.Hook {
	return []ent.Hook{
		hook.On(
			func(next ent.Mutator) ent.Mutator {
				return hook.LoginEventFunc(func(ctx context.Context, m *gen.LoginEventMutation) (ent.Value, error) {
					event := gen.LoginEvent{}
					if outcome, ok := m.Outcome(); ok {
						event.Outcome = outcome
					}

					if err := event.Validate(); err != nil {
						return nil, err
					}

					return next.Mutate(ctx, m)
				})
			},
			ent.OpCreate,
		),
	}
}
