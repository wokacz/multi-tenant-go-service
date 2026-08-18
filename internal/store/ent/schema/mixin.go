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
// It replaces the embedded models.Model, including its BeforeCreate hook. v7 rather
// than v4 for the same reason as before: it is time-ordered, so consecutive inserts
// land next to each other in the primary key index instead of scattering across the
// whole B-tree.
type Model struct {
	mixin.Schema
}

func (Model) Fields() []ent.Field {
	return []ent.Field{
		// DefaultFunc cannot report an error, where the GORM hook could. uuid.NewV7
		// fails only when the system random source does, and a process that cannot
		// read randomness is one that must not be minting identifiers or tokens
		// either — so failing loudly here is the honest outcome rather than a
		// silently zero id.
		field.UUID("id", uuid.UUID{}).
			Default(func() uuid.UUID { return uuid.Must(uuid.NewV7()) }).
			Immutable(),

		// UTC, spelled out on every one of these. GORM had NowFunc on the config,
		// which covered the whole connection; ent has no such single place, so the
		// zone belongs on each default or the same row serialises as +02:00 on a
		// laptop and Z on a server.
		// NOT NULL, which is what these always should have been. GORM left them
		// nullable because the struct fields carried no tag saying otherwise, and
		// nothing has ever written a row without them — the column was permissive
		// about a value the application always supplies.
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
