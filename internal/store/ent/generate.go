// Package ent holds the generated client and, under schema/, the schema it is
// generated from.
//
// The generated code is committed. It is read in review the same way the generated
// Angular client is: a diff that shows what changed is worth more than a build step
// nobody watches, and nothing here should have to run a generator to compile.
//
// Feature flags are enabled up front rather than when each is first needed, because
// turning one on regenerates every file — so the diff of a real change would arrive
// mixed with a thousand lines of unrelated churn:
//
//   - sql/lock lets a query take SELECT ... FOR UPDATE, which the last-owner rule
//     depends on: the owner count and the write that follows have to see the same
//     rows.
//   - sql/upsert gives ON CONFLICT DO NOTHING. GrantSystemRole needs exactly that,
//     and the reason is written down in that method: catching a unique violation
//     inside a transaction aborts the whole transaction in Postgres.
//   - sql/modifier is the escape hatch for the statements that cannot be expressed
//     as a builder call — the attempt counter's CASE WHEN, the ::inet cast, the
//     correlated subquery counting owners.
package ent

// The generator is built from tools/entgen — a module of its own, see the comment
// there — and run from the repository root so entc resolves ./schema inside this
// module. `task ent:generate` does both steps; this directive is here so that
// `go generate` still works for anybody who reaches for it.
//go:generate task ent:generate
