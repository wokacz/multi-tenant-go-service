# Model danych

## Schemat ent jest źródłem prawdy

Schemat bazy wynika ze schematu ent, nie odwrotnie.

```
internal/store/ent/schema/*.go
        │
        ▼  ent generuje klienta i opis tabel
   ent odtwarza migrations/ na bazie roboczej
        │
        ▼  Atlas renderuje różnicę jako SQL
migrations/NNNNNNNNNNNNNN_nazwa.sql
```

```bash
task ent:generate                         # po każdej zmianie schematu
task migrate:diff NAME=cos_sie_zmienilo   # wygeneruj migrację
task migrate                              # zastosuj
```

Migrację liczy **ent**, nie zewnętrzny dostawca schematu: odtwarza katalog `migrations/` na jednorazowej bazie, żeby
poznać stan obecny, porównuje go ze schematem i oddaje różnicę Atlasowi do wyrenderowania. Odtwarzanie, a nie
podłączanie się do żywej bazy, jest tym, co czyni wynik niezależnym od tego, na której maszynie go wygenerowano.

**Automatyczna migracja nie jest wywoływana nigdzie** poza `cmd/entmigrate -apply`, które istnieje tylko po to, żeby
`task schema:compare` mogło porównać dwie bazy. Automatyczna migracja to sposób, żeby w piątek odkryć, że w produkcji
zniknęła kolumna. CI przewraca build (`task ent:check`), gdy schemat zmienił się bez regenerowania klienta.

Krok po kroku: [instrukcja modeli i migracji](../guides/003_models_and_migrations.md).

Katalog trzyma **jedną** migrację bazową, dopóki nic nie jest wdrożone — i dlaczego to przestaje być darmowe
po pierwszym wdrożeniu: [instrukcja 003](../guides/003_models_and_migrations.md#zgniatanie-historii-dopóki-nic-nie-jest-wdrożone).

## Mixiny

Id, czasy i (gdzie trzeba) `deleted_at` / `is_protected` pochodzą z mixinów w `internal/store/ent/schema`.
Wygenerowane typy w `internal/store/ent` są jedynymi modelami; repozytorium oddaje je wprost.

Domyślne `id` to **UUIDv7**, nie v4. v7 jest uporządkowane w czasie, więc kolejne wstawienia lądują obok siebie w
indeksie klucza głównego zamiast rozsypywać się po całym B-drzewie. Skutek uboczny, z którego korzystają listowania:
sortowanie po `id DESC` to sortowanie po czasie utworzenia, bez dodatkowego indeksu.

`IsProtected` blokuje usunięcie w repozytorium (`RefuseIfProtected`), nie w mixinowym hooku DELETE — ten hook nie widzi
kolumny bez osobnego SELECT-a. Korzysta z tego organizacja domyślna i konta systemowe.

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

Wyrażone w schemacie ent, więc schemat zostaje źródłem prawdy:

```go
field.String("email").NotEmpty().
	Annotations(entsql.IndexWhere("deleted_at IS NULL"))
```

Adres i slug **zostają na starym wierszu** — nie są anonimizowane — bo dziennik zmian rozwiązuje aktora `LEFT JOIN`-em do
`users` i bez nich przestałby odpowiadać na pytanie „kto to zrobił". Konsekwencja, o której trzeba wiedzieć: po zwolnieniu
adresu ktoś inny może go zarejestrować, więc dwa wiersze mogą mieć ten sam adres — jeden usunięty, jeden żywy. Zapytania
szukające po adresie muszą iść przez klienta ent (interceptor miękkiego usuwania), nie przez surowy SQL bez filtra.

Dla sluga jest to bezpieczne, bo **nic nie adresuje organizacji slugiem** — każda trasa bierze id. Gdyby kiedyś zaczęło,
ponowne użycie sluga sprawiłoby, że stary link wskazuje innego najemcę, i ta decyzja wymagałaby ponownego rozważenia.

## Pułapki, które już raz kosztowały

### Miękkie usuwanie nie odpala kaskady

`ON DELETE CASCADE` działa wyłącznie przy twardym usunięciu. Dlatego
`User.Delete` sam odwołuje urządzenia konta. Interceptor filtruje odczyty przez klienta ent; surowy SQL i krawędź
`HasUser()` **nie** ukrywają usuniętego konta — trzeba `HasUserWith(DeletedAtIsNil())` albo jawnego `deleted_at`.

### Indeks złożony jest w schemacie, nie na strukturze domeny

Indeksy żyją w `internal/store/ent/schema`. Test Postgresowy czyta `pg_indexes`; `schema:compare` pilnuje, że migracje
i schemat mówią to samo. Nie ma już cichej degradacji do jednej kolumny przez tag, którego osadzany typ nie dziedziczy.

### `NULL` w indeksie unikalnym nie jest duplikatem

W Postgresie dwa `NULL`-e nie kolidują, więc indeks unikalny po kolumnie opcjonalnej nie wymusza tego, czego się po nim
spodziewamy. Stąd
`Role.OrganizationID` jest `NOT NULL`, a role platformowe nie są wierszami w
`roles` — mieszkają w katalogu w kodzie.

### Hook waliduje cały wiersz tylko przy Create

`Validate()` na wygenerowanym typie wymaga kompletnego wiersza. Update, który rusza jedno pole, sprawdza tylko to pole — rename nie może
paść jako „invalid slug". Hooki są w `internal/store/ent/schema/validate.go` i wołają te same metody, których używa
atrapa.

## Enumeracje

Wartość enumeracyjna to wygenerowany typ stringowy z `Valid()`, walidacją przy zapisie **i** ograniczeniem `CHECK` w
bazie — reguła istnieje więc i w Go, i w Postgresie. Stałe (`MembershipActive`, `OutcomeSuccess`, …) są aliasowane w
pakiecie `ent`, żeby wołający nie importował podpakietów tabel.

```go
func (s Status) Valid() bool { return StatusValidator(s) == nil }
func (s Status) GrantsPermissions() bool { return s == StatusActive }
```

`GrantsPermissions` jest metodą, a nie porównaniem `== MembershipActive`
rozsianym po kodzie — to właśnie takie porównania się rozjeżdżają, aż któreś zostanie zapisane jako
`!= MembershipSuspended` i nowy status po cichu zacznie nadawać uprawnienia.

Zaproszenie **nie jest** członkostwem: mieszka we własnej tabeli, trzyma tożsamość na kolumnie `email` i nie ma
`user_id`. Unikalność `(organization_id, email)` zamyka orakl rejestracji. Członkostwo zawsze ma konto — `user_id` jest
`NOT NULL`.

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
| `memberships`                              | kto należy do której organizacji i w jakim stanie                                             |
| `invitations`, `invitation_roles`          | oferty członkostwa; tożsamością jest hash tokenu, nie adres                                   |
| `roles`, `role_permissions`                | role organizacji i to, co nadają                                                              |
| `membership_roles`                         | przypisania ról                                                                               |
| `user_system_roles`                        | role platformowe, przypisywane kluczem                                                        |
| `authz_events`                             | dziennik zmian uprawnień                                                                      |

Każdy nowy schemat trafia do opisu automatycznie: `ent generate` czyta cały katalog `schema/`. Poprzedni układ wymagał
wpisania każdego modelu do ręcznej listy, a pominięty był po cichu nieobecny w generowanym schemacie — po czym Atlas
proponował **usunięcie** jego tabeli.
