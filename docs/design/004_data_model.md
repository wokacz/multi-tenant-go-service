# Model danych

## Modele GORM są źródłem prawdy

Schemat bazy wynika z modeli, nie odwrotnie.

```
internal/store/models/*.go
        │
        ▼  loader/ drukuje DDL
   Atlas diffuje z migrations/
        │
        ▼
migrations/NNNNNNNNNNNNNN_nazwa.sql
```

```bash
task migrate:diff NAME=cos_sie_zmienilo   # wygeneruj migrację po zmianie modelu
task migrate                              # zastosuj
```

**`AutoMigrate` nie jest wywoływane nigdzie.** Zgaduje zmiany kolumn i nigdy nic nie usuwa, więc schemat rozjeżdżałby
się po cichu. CI przewraca build, gdy model zmienił się bez migracji.

Krok po kroku: [instrukcja modeli i migracji](../guides/003_models_and_migrations.md).

Katalog trzyma **jedną** migrację bazową, dopóki nic nie jest wdrożone — i dlaczego to przestaje być darmowe
po pierwszym wdrożeniu: [instrukcja 003](../guides/003_models_and_migrations.md#zgniatanie-historii-dopóki-nic-nie-jest-wdrożone).

## Typy bazowe

```go
type Model struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

type SoftDelete struct {
	DeletedAt   gorm.DeletedAt `gorm:"index"`
	IsProtected bool
}
```

`BeforeCreate` nadaje **UUIDv7**, nie v4. v7 jest uporządkowane w czasie, więc kolejne wstawienia lądują obok siebie w
indeksie klucza głównego zamiast rozsypywać się po całym B-drzewie. Skutek uboczny, z którego korzystają listowania:
sortowanie po `id DESC` to sortowanie po czasie utworzenia, bez dodatkowego indeksu.

`IsProtected` blokuje usunięcie w hooku `BeforeDelete`. Korzysta z tego organizacja domyślna i konta systemowe.

## Kto ma miękkie usuwanie

| Model                  | Usuwanie                            |
|------------------------|-------------------------------------|
| `User`, `Organization` | miękkie (`SoftDelete`)              |
| `Role`                 | **twarde**                          |
| pozostałe              | twarde, kaskadowo przez klucze obce |

`Role` celowo nie ma miękkiego usuwania. Indeks unikalny to
`(organization_id, key)`, a miękko usunięty wiersz nadal zajmuje swój klucz — usunięcie roli `editor` i utworzenie jej
ponownie kończyłoby się błędem duplikatu dla roli, której użytkownik nie widzi. Zamiast tego serwis odmawia usunięcia
roli, którą ktoś jeszcze posiada, co jest lepszą gwarancją niż cofnięcie, do którego nikt nie ma dostępu.

### Unikat przy miękkim usuwaniu musi być częściowy

`users.email` i `organizations.slug` mają unikat **tylko wśród żywych wierszy**:

```sql
CREATE UNIQUE INDEX "idx_users_email" ON "users" ("email") WHERE (deleted_at IS NULL);
```

To ta sama pułapka, którą przy `Role` rozwiązano rezygnacją z miękkiego usuwania — tu rozwiązana drugim sposobem, bo
historia konta i organizacji ma przeżyć usunięcie. Przy zwykłym unikacie usunięte konto **zajmowało swój adres na
zawsze**: nikt nie mógł go zarejestrować ponownie, a ponieważ rejestracja ukrywa duplikat pod `204` (żeby status nie
służył do odgadywania, które adresy istnieją), osoba próbująca dostawała informację o sukcesie i **nigdy** nie mogła się
zalogować. Żaden błąd nigdzie tego nie wyjaśniał. Przy organizacji objawem było `409 slug_taken`, na które nie dało się
zareagować.

Wyrażone tagiem GORM-a, więc model zostaje źródłem prawdy:

```go
Email string `gorm:"size:255;not null;index:idx_users_email,unique,where:deleted_at IS NULL"`
```

Adres i slug **zostają na starym wierszu** — nie są anonimizowane — bo dziennik zmian rozwiązuje aktora `LEFT JOIN`-em do
`users` i bez nich przestałby odpowiadać na pytanie „kto to zrobił". Konsekwencja, o której trzeba wiedzieć: po zwolnieniu
adresu ktoś inny może go zarejestrować, więc dwa wiersze mogą mieć ten sam adres — jeden usunięty, jeden żywy. Zapytania
szukające po adresie muszą przechodzić przez model (zakres soft delete GORM-a), nie przez `Table(...)`.

Dla sluga jest to bezpieczne, bo **nic nie adresuje organizacji slugiem** — każda trasa bierze id. Gdyby kiedyś zaczęło,
ponowne użycie sluga sprawiłoby, że stary link wskazuje innego najemcę, i ta decyzja wymagałaby ponownego rozważenia.

## Pułapki, które już raz kosztowały

### Miękkie usuwanie nie odpala kaskady

`ON DELETE CASCADE` działa wyłącznie przy twardym usunięciu. Dlatego
`User.BeforeDelete` sam odwołuje urządzenia konta, a zapytania czytające przez relacje muszą filtrować `deleted_at`
**jawnie**, jeśli budowane są przez
`Table(...)` zamiast przez model — zakres soft delete GORM-a wtedy nie działa.

### Indeks złożony wymaga przesłonięcia `CreatedAt`

GORM tworzy indeks złożony tylko wtedy, gdy kilka pól dzieli tę samą nazwę indeksu, a osadzone `Model.CreatedAt` nie da
się otagować per model. Trzeba je przesłonić na konkretnej strukturze:

```go
type AuthzEvent struct {
	Model
	CreatedAt time.Time `gorm:"index:idx_authz_org_time,priority:2"`
	OrganizationID *uuid.UUID `gorm:"type:uuid;index:idx_authz_org_time,priority:1"`
	...
}
```

Bez tego indeks po cichu degraduje się do jednokolumnowego. `schema_test.go`
sprawdza kształt indeksów przez `schema.Parse` — w pamięci, bez bazy.

### `NULL` w indeksie unikalnym nie jest duplikatem

W Postgresie dwa `NULL`-e nie kolidują, więc indeks unikalny po kolumnie opcjonalnej nie wymusza tego, czego się po nim
spodziewamy. Stąd
`Role.OrganizationID` jest `NOT NULL`, a role platformowe nie są wierszami w
`roles` — mieszkają w katalogu w kodzie.

### Statement-level `UPDATE` nie widzi danych w hooku

`Model(&T{}).Where(...).Updates(map)` uruchamia `BeforeSave` na **zerowej**
strukturze, więc walidacja w hooku odrzuci poprawną zmianę. Takie zapytania jawnie pomijają hooki i polegają na
walidacji w serwisie oraz na ograniczeniach kolumny. Szczegóły: [praca z bazą](../guides/005_database_access.md).

## Enumeracje

Wartość enumeracyjna to typ stringowy z metodą `Valid()`, hookiem `BeforeSave`
odrzucającym nieznaną wartość **i** ograniczeniem `check:` w tagu — reguła istnieje więc i w Go, i w Postgresie.

```go
type MembershipStatus string

const (
	MembershipInvited   MembershipStatus = "invited"
	MembershipActive    MembershipStatus = "active"
	MembershipSuspended MembershipStatus = "suspended"
)

func (s MembershipStatus) Valid() bool { ... }
func (s MembershipStatus) GrantsPermissions() bool { return s == MembershipActive }
```

`GrantsPermissions` jest metodą, a nie porównaniem `== MembershipActive`
rozsianym po kodzie — to właśnie takie porównania się rozjeżdżają, aż któreś zostanie zapisane jako
`!= MembershipSuspended` i zaproszenie po cichu stanie się członkostwem.

Zaproszenie trzyma tożsamość na kolumnie `email` i zostawia `user_id` puste, dopóki zaproszony nie przyjmie. Unikalność
`(organization_id, email)` zamyka orakl rejestracji: zaproszenie nie musi najpierw szukać adresu w `users`. Indeks
`(user_id, organization_id)` zostaje — dwa `NULL` w Postgresie się nie zderzają, więc wiele zaproszeń do jednej
organizacji jest legalne.

Wyjątkiem jest `AuthzAction`, która nie ma ograniczenia w bazie: lista rośnie z każdą operacją administracyjną, a
`check` zamieniałby każde dopisanie w migrację. Tabela jest append-only i zapisywana z jednego miejsca, więc walidacja
po stronie Go jest tym, co ją naprawdę pilnuje.

## Spis tabel

| Tabela                                     | Zawiera                                                                                       |
|--------------------------------------------|-----------------------------------------------------------------------------------------------|
| `users`                                    | konta, epoka sesji, drugi składnik, zawieszenie, język                                        |
| `devices`                                  | znane urządzenia, odcisk `SHA-256`, zaufanie, odwołanie                                       |
| `login_events`                             | historia logowań                                                                              |
| `password_resets`, `two_factor_challenges`, `email_changes` | kody jednorazowe (HMAC z osobnym `purpose`, TTL, licznik prób)               |
| `organizations`                            | najemcy                                                                                       |
| `memberships`                              | kto należy do której organizacji i w jakim stanie; zaproszenie trzyma adres i puste `user_id` |
| `roles`, `role_permissions`                | role organizacji i to, co nadają                                                              |
| `membership_roles`                         | przypisania ról                                                                               |
| `user_system_roles`                        | role platformowe, przypisywane kluczem                                                        |
| `role_translations`                        | tłumaczenia nazw ról własnych — **tabela jeszcze nieużywana**                                 |
| `authz_events`                             | dziennik zmian uprawnień                                                                      |

Każdy nowy model musi trafić do listy w `loader/main.go`. Pominięty jest po cichu nieobecny w generowanym schemacie, a
Atlas zaproponuje **usunięcie** jego tabeli.
