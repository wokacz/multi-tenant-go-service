# Modele i migracje

Schemat ent jest źródłem prawdy dla bazy. Migracje z niego **wynikają**, nie odwrotnie.

## Lista kontrolna

1. Schemat w [`internal/store/ent/schema/`](../../internal/store/ent/schema) — jeden plik na encję.
2. `task ent:generate` — bez tego kod klienta nie wie o zmianie, a `task check` przewraca build (`ent:check`).
3. `task migrate:diff NAME=cos` — migracja.
4. `task migrate` — zastosowanie.
5. Jeśli domena ma o wierszu wiedzieć, używa wygenerowanego typu z [`internal/store/ent`](../../internal/store/ent).
   Typy entgo.io (klient, mutacje) **nie wychodzą** z `internal/store` (`TestEntStaysInsideTheStore`); same struktury
   `ent.User`, `ent.Organization` są modelami i nie ma drugiej kopii.

Kroku „dopisz encję do listy" **nie ma**: `ent generate` czyta cały katalog `schema/`. Poprzedni układ wymagał wpisania
każdego modelu do ręcznej listy w osobnym module, a pominięty był po cichu nieobecny w schemacie — po czym Atlas
proponował usunięcie jego tabeli.

## Schemat

```go
// internal/store/ent/schema/widget.go
type Widget struct {
	ent.Schema
}

// Mixin daje id (UUIDv7), created_at i updated_at. SoftDelete dokłada deleted_at
// i is_protected — tylko tam, gdzie miękkie usuwanie ma sens.
func (Widget) Mixin() []ent.Mixin {
	return []ent.Mixin{Model{}}
}

func (Widget) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("organization_id", uuid.UUID{}),
		field.String("key").MaxLen(64).NotEmpty(),
		field.String("name").MaxLen(100).NotEmpty(),

		// Optional() to kolumna NULL-owalna; Nillable() dokłada wskaźnik w Go, żeby
		// „brak wartości" dało się odróżnić od „wartość zerowa".
		field.String("note").MaxLen(255).Optional(),
	}
}

func (Widget) Edges() []ent.Edge {
	return []ent.Edge{
		// Krawędź daje kolumnę FK **i** constraint. Field() nazywa kolumnę, żeby
		// zapytania pisane ręcznie miały do czego się odwołać.
		edge.From("organization", Organization.Type).
			Ref("widgets").
			Field("organization_id").
			Unique().
			Required().
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (Widget) Indexes() []ent.Index {
	return []ent.Index{
		// StorageKey, bo nazwa indeksu jest czytana przez testy schematu i przez
		// człowieka szukającego w EXPLAIN. Bez niej ent nadaje własną.
		index.Fields("organization_id", "key").
			Unique().
			StorageKey("idx_widget_org_key"),
	}
}
```

Dwie rzeczy, które ent robi inaczej, niż wygląda:

- **`MaxLen(n)` to walidator w Go, nie szerokość kolumny.** W Postgresie kolumna jest `character varying` bez długości.
  Świadomie tak zostało: długość pilnuje Go, tam gdzie mieszkają wszystkie inne reguły długości w tym kodzie.
- **`Optional()` na czasie nie daje wskaźnika.** Bez `Nillable()` dostajesz `time.Time` i zero jako „brak".

## Reguły: hooki i walidatory

Walidacja pola idzie w pole (`NotEmpty()`, `MaxLen`, `Validate(func)`), a reguła obejmująca cały wiersz — w hook.
To odpowiednik dawnych `BeforeSave`: sprawdzenia, które muszą zachodzić niezależnie od tego, kto zapisuje.

Miękkie usuwanie jest mixinem z **interceptorem** (filtruje odczyty) i **hookiem** (zamienia DELETE na UPDATE), plus
`SkipSoftDelete(ctx)` dla ścieżek, które muszą zobaczyć wiersz usunięty — `RemoveMember` działa na członkostwie, którego
konto zniknęło, i to jest jedyny sposób posprzątania takiego wiersza.

## Migracja

```bash
task migrate:diff NAME=widgets
task migrate
```

Różnicę liczy **ent**: odtwarza `migrations/` na jednorazowej bazie, żeby poznać stan obecny, porównuje ze schematem i
oddaje wynik Atlasowi do wyrenderowania jako SQL. Odtwarzanie, a nie podłączanie się do żywej bazy, jest tym, co czyni
migrację niezależną od maszyny, na której powstała.

Migracji **nie pisze się ręcznie** — ręczna edycja unieważnia `atlas.sum`. Wyjątek: `ADD COLUMN … NOT NULL` na tabeli,
która ma już wiersze. Diff tego nie umie uzupełnić danymi; wtedy dopisuje się `UPDATE` i woła `atlas migrate hash`.

Generator **nie usuwa** kolumn ani indeksów sam z siebie (`WithDropColumn(false)`, `WithDropIndex(false)`): usunięcie to
decyzja, a generator, który podejmuje ją po cichu, kiedyś podejmie ją na złej gałęzi.

Sprawdzenie przed pull requestem:

```bash
task check            # tidy, lint, test, ent:check, openapi:check
```

`ent:check` pilnuje, że wygenerowany klient zgadza się ze schematem. Testy postgresowe w CI sprawdzają, że repozytoria
działają na prawdziwym schemacie po `atlas migrate apply`.

## Zgniatanie historii, dopóki nic nie jest wdrożone

Katalog zawiera **jedną** migrację — `..._baseline.sql` — i tak ma zostać, dopóki repozytorium nie ma wdrożonej bazy.
Osiem plików opisujących drogę do jednego schematu to osiem plików do przeczytania, żeby poznać jeden schemat, a kolejność
kroków, których żadna baza nie wykonała, nie jest historią, tylko szumem.

```bash
rm -f migrations/*.sql migrations/atlas.sum
task migrate:diff NAME=baseline
task check
```

Dwie rzeczy przy tym giną i trzeba o nich wiedzieć:

- **Backfille danych.** `ADD COLUMN … NOT NULL` na tabeli z wierszami wymaga dopisanego `UPDATE` (patrz wyżej). Baseline
  startuje od zera wierszy, więc taki krok jest zbędny — ale jeśli zgniatasz historię, w której był, sprawdź, czy nie
  zawierał **czegoś więcej** niż uzupełnienie kolumny.
- **Stany przejściowe.** Kolumna, która była `NOT NULL`, a potem stała się nullowalna, w baseline jest po prostu
  nullowalna. To dobrze — model mówi dokładnie to.

**Od momentu pierwszego wdrożenia zgniatanie przestaje być darmowe.** Atlas zapisuje w bazie zastosowane wersje; po
zgnieceniu te wersje nie istnieją i `task migrate` odmówi. Wtedy jedyną drogą jest `atlas migrate baseline`, a nie
usunięcie plików.

## Czego nie robić

- **Nie wołaj automatycznej migracji w aplikacji.** Serwis nie tworzy ani nie zmienia schematu przy starcie — tylko
  `atlas migrate apply` na wdrożeniu i `task migrate:diff` przy zmianie schematu.
- **Nie licz na kaskadę przy miękkim usuwaniu.** `ON DELETE CASCADE` odpala się tylko przy twardym; przy miękkim
  posprzątaj w hooku albo filtruj `deleted_at` w zapytaniu.
- **Nie zakładaj, że indeks unikalny po kolumnie NULL-owalnej coś wymusza.** W Postgresie dwa `NULL`-e nie kolidują.
