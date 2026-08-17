# Praca z bazą

Pułapki, które w tym projekcie już raz kosztowały czas. Każda ma test, który pilnuje, żeby nie wróciła.

## Połączenie

`store.OpenPostgres` ustawia rzeczy, na które kod liczy:

| Ustawienie                                          | Po co                                                                                       |
|-----------------------------------------------------|---------------------------------------------------------------------------------------------|
| `TranslateError: true`                              | bez tego nie ma `gorm.ErrDuplicatedKey`, a repozytorium nie rozpozna naruszenia unikalności |
| `NowFunc` w UTC                                     |                                                                                             |
| `DisableAutomaticPing`                              | ping robimy sami, pod własnym timeoutem                                                     |
| logger na poziomie `Warn` z progiem wolnych zapytań |                                                                                             |

`time.Local = time.UTC` w `main` jest konieczne, bo pgx dekoduje `timestamptz`
do strefy lokalnej — bez tego ten sam wiersz serializowałby się inaczej na laptopie i na serwerze.

## Liczniki ruszaj w SQL-u, nie w Go

**Źle:**

```go
row := load(id)
row.Attempts++
save(row)
```

Nakładające się żądania odczytają tę samą wartość i zapiszą tę samą wartość, więc pięć równoległych prób zostawia
licznik na jedynce. Spóźniony zapis może nawet przywrócić `consumed_at`, które inne żądanie właśnie ustawiło.

**Dobrze** — jeden warunkowy `UPDATE`:

```go
r.db.WithContext(ctx).
Model(&models.PasswordReset{}).
Where("id = ? AND consumed_at IS NULL", resetID).
Updates(map[string]any{
"attempts":    gorm.Expr("attempts + 1"),
"consumed_at": gorm.Expr("CASE WHEN attempts + 1 >= ? THEN ?::timestamptz ELSE consumed_at END",
maxAttempts, now),
})
```

Trzy szczegóły, które są nośne:

1. każde wyrażenie `SET` czyta wiersz **sprzed** `UPDATE`, więc `attempts + 1`
   musi być powtórzone w `CASE`;
2. rzutowanie `?::timestamptz` jest jawne;
3. zero zmienionych wierszy **nie jest błędem** — wołający i tak zwróci błąd „zły kod".

Pilnuje tego `TestFailPasswordResetUnderConcurrency` na prawdziwym Postgresie.

## Hooki a `UPDATE` bez wiersza

`Model(&T{}).Where(...).Updates(map)` uruchamia `BeforeSave` na **zerowej**
strukturze. Walidacja w hooku zobaczy puste pola i odrzuci poprawną zmianę.

Rozwiązanie: jawnie pomiń hooki i polegaj na walidacji w serwisie oraz na ograniczeniach kolumny.

```go
r.db.WithContext(ctx).
Session(&gorm.Session{SkipHooks: true}).
Model(&models.Organization{}).
Where("id = ?", orgID).
Update("name", name)
```

`SkipHooks` zachowuje `updated_at`, w przeciwieństwie do `UpdateColumns`.

**Odwrotny przypadek:** gdy hook niesie regułę, której nie chcesz stracić (ochrona przed usunięciem), wczytaj wiersz i
usuń **obiekt**:

```go
org, err := r.Organization(ctx, orgID) // BeforeDelete zobaczy IsProtected
if err != nil {
return err
}

return r.db.WithContext(ctx).Delete(org).Error
```

## Transakcje

Atomowość jest własnością jednej metody repozytorium:

```go
err := r.db.WithContext(ctx).Transaction(func (tx *gorm.DB) error {
if err := tx.Where("membership_id = ?", memberID).
Delete(&models.MembershipRole{}).Error; err != nil {
return err
}

if err := assignRoles(tx, orgID, memberID, roleIDs); err != nil {
return err
}

return record(ctx, tx, &models.AuthzEvent{ ... }) // audyt w tej samej transakcji
})
```

Wewnątrz używaj **`tx`**, nigdy `r.db`. Zapis poza transakcją to dokładnie ten audyt, który zostaje po cofniętej
zmianie.

## Zastępuj, nie doklejaj

Kolekcje (role członka, uprawnienia roli) zapisuje się przez **zastąpienie całości** w jednej transakcji, a endpointy
używają `PUT`, nie `PATCH`.

Dwie równoczesne edycje w wariancie przyrostowym cicho zlewają się w zbiór, którego nie wybrał żaden z administratorów.
W wariancie zastępującym ostatni wygrywa — w sposób, który da się wyjaśnić.

## Ostatni właściciel zamyka się w transakcji

Sprawdzenie „czy to ostatni właściciel" i mutacja, która odbiera tę zdolność, muszą iść **w jednej transakcji**, z
`SELECT … FOR UPDATE` na wierszu organizacji. Dwa nakładające się zdegradowania oba widzą `owners > 1` i oba przechodzą,
jeśli locka nie ma. `TestConcurrentDemotionsLeaveOneOwner` (Postgres) jest tym, co to pilnuje.

## Zawężenie idzie do `WHERE`

```go
// dobrze — wiersz z cudzej organizacji nie zostaje znaleziony
First(&role, "id = ? AND organization_id = ?", roleID, orgID)

// źle — znaleziony i odrzucony; łatwo zapomnieć o drugim kroku
First(&role, "id = ?", roleID)
if role.OrganizationID != orgID { ... }
```

Klucz obcy **nie wystarcza** do sprawdzenia przynależności: mówi tylko, że wiersz gdzieś istnieje. Przypisanie roli z
cudzej organizacji przechodzi walidację FK bez problemu, dlatego `assignRoles` liczy, ile z podanych identyfikatorów
należy do tej organizacji, i odmawia, gdy nie wszystkie.

## Miękkie usuwanie i zapytania po nazwie tabeli

Zakres soft delete GORM-a działa tylko przy zapytaniu **przez model**. Zapytanie budowane przez `Table("...")` musi
filtrować `deleted_at` samo:

```go
Table("memberships AS m").
Joins("JOIN organizations o ON o.id = m.organization_id AND o.deleted_at IS NULL").
Joins("LEFT JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL")
```

Lista członków musi być `LEFT JOIN` do `users`: zaproszenie nie ma `user_id`, a `INNER JOIN` wyciąłby je z listy.
Zaproszenie i tak nic nie nadaje — `GrantsPermissions` wymaga statusu `active`.

Pominięcie `deleted_at` na użytkowniku oznacza, że usunięte konto nadal ma uprawnienia.

## `LEFT JOIN` kontra `INNER JOIN`

Wybór bywa różnicą między dwoma różnymi statusami HTTP.

Zapytanie rozwiązujące uprawnienia używa `LEFT JOIN`, bo musi odróżnić **członka bez ról** (`403`) od **kogoś spoza
organizacji** (`404`). `INNER JOIN`
zwraca w obu przypadkach zero wierszy i skleiłby je w jedną odpowiedź.

Zapytanie zasilające migawkę uprawnień używa `INNER JOIN`, bo organizacja, w której wołający nic nie ma, nie daje UI nic
do odblokowania.

## Unikaj N+1

Wzorzec: pobierz wiersze główne, zbierz identyfikatory, dociągnij resztę **jednym** zapytaniem `IN`, złóż w Go.

```go
rows := ... // 1 zapytanie
ids := collectIDs(rows)
extras := queryIn(ids) // 1 zapytanie
index := indexBy(rows)
for _, extra := range extras { ... }
```

Tak działają `attachRoles`, `decorateRoles` i `MembershipsForUser`. Jedno złączenie zwracające iloczyn kartezjański też
by zadziałało, ale wiersze i tak trzeba by składać w Go — a to jest kształt, który przy pierwszym dodanym polu zamienia
się w N+1.

## Naruszenie unikalności zrywa transakcję

W Postgresie `unique_violation` ustawia transakcję w stan aborted. Kolejne polecenie na tym samym `tx` kończy się
`25P02`, nawet jeśli Go obsłużył błąd. Żeby po kolizji insertu zrobić jeszcze `SELECT` (na przykład przejąć
zaległe zaproszenie), potrzebny jest savepoint: `tx.SavePoint(...)` przed insertem i `tx.RollbackTo(...)` po
`gorm.ErrDuplicatedKey`. `SavePoint` i `RollbackTo` zwracają `*gorm.DB` — błąd jest w `.Error`.

## Stronicowanie

Serwis **przycina** limit, zamiast ufać wołającemu:

```go
if limit <= 0 || limit > MaxPage {
limit = MaxPage
}
```

Sortowanie po `id DESC` jest sortowaniem po czasie utworzenia — UUIDv7 jest uporządkowane w czasie, więc nie potrzeba
dodatkowego indeksu.
