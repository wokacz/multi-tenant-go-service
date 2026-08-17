# Modele i migracje

Modele GORM są źródłem prawdy dla schematu. Migracje z nich **wynikają**, nie odwrotnie.

## Lista kontrolna

1. Model w `internal/store/models/<rzecz>.go`
2. Dopisz go do `Load(...)` w `loader/main.go`
3. `task migrate:diff NAME=<opis>` i `task migrate`
4. Test kształtu indeksów, jeśli dodałeś indeks złożony lub unikalny
5. Test hooków, jeśli dodałeś regułę w Go

Krok 2 jest tym, który boli po cichu: model pominięty w loaderze **nie istnieje**
w generowanym schemacie, a Atlas zaproponuje **usunięcie** jego tabeli.

## Model

```go
type Widget struct {
Model      // ID (UUIDv7), CreatedAt, UpdatedAt
SoftDelete // tylko gdy potrzebujesz miękkiego usuwania

OrganizationID uuid.UUID     `gorm:"type:uuid;not null;uniqueIndex:idx_widget_org_key,priority:1"`
Organization   *Organization `json:"-" gorm:"constraint:OnDelete:CASCADE"`

Key    string  `gorm:"size:64;not null;uniqueIndex:idx_widget_org_key,priority:2"`
Name   string  `gorm:"size:100;not null"`
Note   *string `gorm:"size:255"` // wskaźnik = kolumna NULL-owalna
}
```

Konwencje:

| Zasada                              | Dlaczego                                                                     |
|-------------------------------------|------------------------------------------------------------------------------|
| jawne `size:` i `not null`          | inaczej dostajesz `text` i kolumnę opcjonalną, o której nikt nie zdecydował  |
| `type:uuid` dla identyfikatorów     |                                                                              |
| wskaźnik dla wartości opcjonalnej   | „nigdy nie ustawione" to `NULL`, nie `""` — Postgres odrzuca `''` dla `inet` |
| relacja wsteczna z `json:"-"`       | model i tak nie trafia do JSON-a, ale nie zostawiaj wątpliwości              |
| `constraint:OnDelete:CASCADE`       | na relacji wstecznej                                                         |
| reguły biznesowe jako metody modelu | `Device.Revoke()`, `MembershipStatus.GrantsPermissions()`                    |
| błędy jako `models.ErrXxx`          | z prefiksem `models: ` w treści                                              |

## Reguły w hookach

Walidacja, którą da się sprawdzić w Go, idzie do `BeforeSave` — daje sensowny błąd zamiast naruszenia ograniczenia z
Postgresa:

```go
func (w *Widget) BeforeSave(_ *gorm.DB) error {
if w.Name == "" || utf8.RuneCountInString(w.Name) > 100 {
return fmt.Errorf("models: invalid widget name %q", w.Name)
}

return nil
}
```

**Pułapka batch delete.** Hook `BeforeDelete` dostaje zerową strukturę przy usuwaniu bez klucza głównego, więc ochrona
typu `IsSystem` po cichu by nie zadziałała. Wzorzec:

```go
func (r *Role) BeforeDelete(_ *gorm.DB) error {
if r.ID == uuid.Nil {
return ErrRoleBatchDeleteUnsupported
}

if r.IsSystem {
return ErrRoleIsSystem
}

return nil
}
```

Repozytorium musi wtedy **wczytać wiersz przed usunięciem**, a nie robić
`Where(...).Delete(...)`.

## Enumeracje

```go
type WidgetState string

const (
WidgetDraft     WidgetState = "draft"
WidgetPublished WidgetState = "published"
)

func (s WidgetState) Valid() bool { ... }
```

Plus `check:` w tagu kolumny i `BeforeSave` odrzucający nieznaną wartość — reguła istnieje wtedy i w Go, i w bazie.
Wzorzec: `models.LoginOutcome`.

Wyjątek: lista, która rośnie z każdą operacją (jak `AuthzAction`), nie dostaje
`check`, bo zamieniałby każde dopisanie w migrację.

## Indeks złożony wymaga przesłonięcia `CreatedAt`

GORM buduje indeks złożony tylko wtedy, gdy kilka pól dzieli nazwę indeksu, a osadzonego `Model.CreatedAt` nie da się
otagować per model:

```go
type WidgetEvent struct {
Model
CreatedAt time.Time `gorm:"index:idx_widget_time,priority:2"`
WidgetID  uuid.UUID `gorm:"type:uuid;not null;index:idx_widget_time,priority:1"`
}
```

Bez przesłonięcia indeks po cichu degraduje się do jednokolumnowego. Dlatego każdy indeks złożony i unikalny dostaje
asercję w
`internal/store/models/schema_test.go`:

```go
func TestWidgetKeyIsUniquePerOrganization(t *testing.T) {
idx := indexOf(t, &models.Widget{}, "idx_widget_org_key")

want := []string{"organization_id", "key"}
if got := indexColumns(idx); !slices.Equal(got, want) {
t.Errorf("kolumny = %v, want %v", got, want)
}

if idx.Class != "UNIQUE" {
t.Errorf("klasa = %q, want UNIQUE", idx.Class)
}
}
```

Test używa `schema.Parse` w pamięci — **nie potrzebuje bazy**.

## Migracja

```bash
task migrate:diff NAME=widgets
task migrate
```

Atlas porównuje DDL wynikający z modeli z katalogiem `migrations/` i dopisuje plik `NNNNNNNNNNNNNN_widgets.sql`.
Migracji **nie pisze się ręcznie** — ręczna edycja unieważnia `atlas.sum`. Wyjątek: `ADD COLUMN … NOT NULL` na tabeli,
która już ma wiersze. Diff tego nie umie uzupełnić danymi; wtedy dopisuje się `UPDATE` z istniejącej kolumny i woła
`atlas migrate hash`.

Sprawdzenie przed pull requestem:

```bash
atlas migrate validate --env local
atlas migrate diff ci_drift --env local     # "synced" = brak dryfu
```

CI robi dokładnie to i przewraca build, gdy model zmienił się bez migracji.

## Zgniatanie historii, dopóki nic nie jest wdrożone

Katalog zawiera **jedną** migrację — `..._baseline.sql` — i tak ma zostać, dopóki repozytorium nie ma wdrożonej bazy.
Osiem plików opisujących drogę do jednego schematu to osiem plików do przeczytania, żeby poznać jeden schemat, a kolejność
kroków, których żadna baza nie wykonała, nie jest historią, tylko szumem.

```bash
rm -f migrations/*.sql migrations/atlas.sum
task migrate:diff NAME=baseline
atlas migrate diff verify --env local        # "synced" = baseline zgadza się z modelami
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

- **Nie wołaj `AutoMigrate`.** Zgaduje zmiany kolumn i nigdy nic nie usuwa.
- **Nie licz na kaskadę przy miękkim usuwaniu.** `ON DELETE CASCADE` odpala się tylko przy twardym; przy miękkim
  posprzątaj w `BeforeDelete` albo filtruj
  `deleted_at` w zapytaniu.
- **Nie zakładaj, że indeks unikalny po kolumnie NULL-owalnej coś wymusza.** W Postgresie dwa `NULL`-e nie kolidują.
