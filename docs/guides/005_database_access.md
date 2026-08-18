# Praca z bazą

Pułapki, które w tym projekcie już raz kosztowały czas. Każda ma test, który pilnuje, żeby nie wróciła.

## Połączenie

`store.OpenPostgres` otwiera pulę przez pgx (`database/sql`) i buduje na niej klienta ent. Ping jest osobnym krokiem,
pod
`DBConnectTimeout` — `sql.Open` jest leniwe i samo nic nie udowadnia.

Tłumaczenie naruszeń unikalności jest w repozytorium (`isUniqueViolation`, kod Postgresa `23505`), nie w konfiguracji
połączenia. Domyślne czasy w schemacie ent są w UTC.

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

**Dobrze** — jeden warunkowy `UPDATE`, przez `Modify`, bo ent umie `AddAttempts(1)`, ale nie „spal kod, gdy ten
increment dojdzie do limitu":

```go
r.db.Ent().EmailChange.Update().
	Where(emailchange.ID(changeID), emailchange.ConsumedAtIsNil()).
	Modify(func(u *entsql.UpdateBuilder) {
		u.Set(emailchange.FieldAttempts, entsql.ExprFunc(func(b *entsql.Builder) {
			b.Ident(emailchange.FieldAttempts).WriteString(" + 1")
		}))
		u.Set(emailchange.FieldConsumedAt, entsql.ExprFunc(func(b *entsql.Builder) {
			b.WriteString("CASE WHEN ").
				Ident(emailchange.FieldAttempts).
				WriteString(" + 1 >= ").Arg(maxAttempts).
				WriteString(" THEN ").Arg(now).
				WriteString("::timestamptz ELSE ").
				Ident(emailchange.FieldConsumedAt).
				WriteString(" END")
		}))
	}).
	Exec(ctx)
```

Trzy szczegóły, które są nośne:

1. każde wyrażenie `SET` czyta wiersz **sprzed** `UPDATE`, więc `attempts + 1`
   musi być powtórzone w `CASE`;
2. rzutowanie `::timestamptz` jest jawne;
3. zero zmienionych wierszy **nie jest błędem** — wołający i tak zwróci błąd „zły kod".

Pilnuje tego `TestFailPasswordResetUnderConcurrency` na prawdziwym Postgresie.

## Hooki a `UPDATE` jednego pola

Hook create woła `Validate()` na całym wierszu. Update, który rusza jedno pole, **nie** może tego zrobić — rename
organizacji padłby jako „invalid slug". `schema/validate.go` sprawdza wtedy tylko ruszone pole. Serwis i tak waliduje
wcześniej; ograniczenia kolumny są drugą siatką.

**Odwrotny przypadek:** gdy ochrona przed usunięciem musi zobaczyć `IsProtected` / `IsSystem`, wczytaj wiersz i dopiero
wtedy wołaj `RefuseIfProtected` / `RefuseDelete`. Usunięcie po samym id tej wartości nie widzi.

## Transakcje

Atomowość jest własnością jednej metody repozytorium:

```go
err := r.withTx(ctx, func(tx *ent.Tx) error {
	if _, err := tx.MembershipRole.Delete().
		Where(membershiprole.MembershipID(memberID)).
		Exec(ctx); err != nil {
		return err
	}

	if err := assignMembershipRoles(ctx, tx, orgID, memberID, roleIDs); err != nil {
		return err
	}

	return recordEnt(ctx, tx, &ent.AuthzEvent{ ... })
})
```

Wewnątrz używaj **`tx`**, nigdy `r.db.Ent()`. Zapis poza transakcją to dokładnie ten audyt, który zostaje po cofniętej
zmianie.

## Zastępuj, nie doklejaj

Kolekcje (role członka, uprawnienia roli) zapisuje się przez **zastąpienie całości** w jednej transakcji, a endpointy
używają `PUT`, nie `PATCH`.

Dwie równoczesne edycje w wariancie przyrostowym cicho zlewają się w zbiór, którego nie wybrał żaden z administratorów.
W wariancie zastępującym ostatni wygrywa — w sposób, który da się wyjaśnić.

## Reguła w domenie, transakcja w repozytorium

Sprawdzenie „czy to ostatni właściciel" i mutacja, która odbiera tę zdolność, muszą iść **w jednej transakcji**, z
`SELECT … FOR UPDATE` na wierszu organizacji. Dwa nakładające się zdegradowania oba widzą `owners > 1` i oba przechodzą,
jeśli locka nie ma. `TestConcurrentDemotionsLeaveOneOwner` (Postgres) jest tym, co to pilnuje.

Sama reguła nie mieszka jednak w SQL-u. Repozytorium przyjmuje **strażnika** z domeny, bierze blokadę, liczy fakty i
pyta go o werdykt:

```go
func applyOwnerGuard(ctx context.Context, tx *ent.Tx, orgID, memberID uuid.UUID, guard orgs.OwnerGuard) error {
	if err := lockOrganization(ctx, tx, orgID); err != nil {
		return err
	}
	state, err := ownerStateTx(ctx, tx, orgID, memberID)
	if err != nil {
		return err
	}
	return guard(state)
}
```

**Dlaczego tak, a nie regułą w zapytaniu:** reguła zapisana w SQL-u musi być powtórzona w fake'u, a dwie kopie w końcu
się rozjeżdżają. Tak właśnie powstał błąd, w którym członkostwo z usuniętym kontem liczyło się jako właściciel w jednym
zapytaniu i nie liczyło w drugim — wiersza nie dało się usunąć w ogóle. Szczegóły w
[design/007](../design/007_authorization.md).

Kiedy piszesz nową regułę tego typu:

- **strażnik jest wymagany** — brak zmiany zdolności to `RefuseLastOwnerLoss(false)`, nigdy `nil`;
- **blokuj wiersz organizacji**, nie coś innego. Wszystkie serializowane zmiany biorą tę samą blokadę, więc nie ma dwóch
  kolejności i nie ma zakleszczeń;
- **fakty licz wewnątrz transakcji.** Odczyt sprzed niej jest dokładnie tym wyścigiem, który zamykasz.

## Zawężenie idzie do `WHERE`

```go
// dobrze — wiersz z cudzej organizacji nie zostaje znaleziony
tx.Role.Query().Where(role.ID(roleID), role.OrganizationID(orgID)).Only(ctx)

// źle — znaleziony i odrzucony; łatwo zapomnieć o drugim kroku
row, _ := tx.Role.Get(ctx, roleID)
if row.OrganizationID != orgID { ... }
```

Klucz obcy **nie wystarcza** do sprawdzenia przynależności: mówi tylko, że wiersz gdzieś istnieje. Przypisanie roli z
cudzej organizacji przechodzi walidację FK bez problemu, dlatego `assignMembershipRoles` liczy, ile z podanych
identyfikatorów należy do tej organizacji, i odmawia, gdy nie wszystkie.

## Miękkie usuwanie i krawędzie

Interceptor filtruje odczyty encji z `deleted_at`. Nie filtruje `EXISTS` z krawędzi: `HasUser()` jest prawdą także dla
usuniętego konta, a `WithUser` zwraca wtedy `nil`. Predykat to `HasUserWith(user.DeletedAtIsNil())` — ten sam warunek co
`u.deleted_at IS NULL` w SQL.

Surowy SQL na puli (`r.db.SQL()`) interceptora nie widzi. Trzeba napisać filtr samemu, albo świadomie go pominąć.

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
`25P02`, nawet jeśli Go obsłużył błąd. Dlatego `GrantSystemRole` jest
`OnConflictColumns().DoNothing().ID(ctx)` — nie wolno wstawiać i łapać duplikatu wewnątrz transakcji, która ma jeszcze
coś zrobić.

## Stronicowanie

Serwis **przycina** limit, zamiast ufać wołającemu:

```go
if limit <= 0 || limit > MaxPage {
	limit = MaxPage
}
```

Sortowanie po `id DESC` jest sortowaniem po czasie utworzenia — UUIDv7 jest uporządkowane w czasie, więc nie potrzeba
dodatkowego indeksu.
