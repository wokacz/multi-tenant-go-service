package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/mixin"
	"github.com/google/uuid"
)

// Model is what every table has: a UUIDv7 primary key and the two timestamps.
//
// v7 rather than v4: it is time-ordered, so consecutive inserts land next to each
// other in the primary key index instead of scattering across the whole B-tree.
type Model struct {
	mixin.Schema
}

func (Model) Fields() []ent.Field {
	return []ent.Field{
		// DefaultFunc cannot report an error. uuid.Must is honest: NewV7 fails only
		// when the system random source does, and a process that cannot read
		// randomness must not be minting identifiers or tokens either.
		field.UUID("id", uuid.UUID{}).
			Default(func() uuid.UUID { return uuid.Must(uuid.NewV7()) }).
			Immutable(),

		// UTC, spelled out on every default. ent has no connection-wide clock, so
		// the zone belongs here or the same row serialises as +02:00 on a laptop
		// and Z on a server. The columns are NOT NULL: nothing has ever written a
		// row without them.
		field.Time("created_at").
			Default(nowUTC).
			Immutable(),

		field.Time("updated_at").
			Default(nowUTC).
			UpdateDefault(nowUTC),
	}
}

// nowUTC is the one clock these schemas read.
func nowUTC() time.Time {
	return time.Now().UTC()
}
