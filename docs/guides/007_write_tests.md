# Pisanie testów

Wyłącznie `testing` ze standardowej biblioteki. Bez testify, bez biblioteki mockującej, bez plików golden. Asercje to
zwykłe `if`:

```go
if got != want {
t.Errorf("Nazwa() = %v, want %v", got, want)
}
```

Komunikat mówi **co było i czego oczekiwano**, a przy testach nieoczywistych — także dlaczego to ma znaczenie:

```go
t.Fatalf("attempts = %d, want %d — nakładające się próby zostały zgubione", got, want)
```

## Cztery poziomy

| Poziom                     | Gdzie                                                         | Potrzebuje bazy |
|----------------------------|---------------------------------------------------------------|-----------------|
| strukturalny / kontraktowy | `internal/architecture_test.go`, `internal/api/authz_test.go` | nie             |
| modelowy                   | `internal/store/ent/rules_test.go`                            | nie             |
| domenowy                   | `internal/domain/*/…_test.go` (fake)                          | nie             |
| HTTP                       | `internal/api/httptest/*_http_test.go` (pełny router)         | nie             |
| SQL                        | `internal/store/repositories/*_postgres_test.go`              | **tak**         |
| kontraktowy (obie impl.)   | `internal/store/repositories/contract/`                       | do połowy       |

Reguła doboru: **jeśli test przeszedłby na fake'u, jego miejsce jest na fake'u.**
Testy na Postgresie zostawiamy dla tego, co fake udaje w Go. A to, o czym **obie implementacje muszą mówić to samo**,
idzie do zestawu kontraktowego — patrz niżej.

## Testy strukturalne

Najcenniejsze w tym projekcie: dowodzą, że czegoś **nie da się** pominąć, a nie że akurat działa. Wzorce, z których
warto korzystać przy nowym module:

```go
// Wyliczenie po zarejestrowanych operacjach.
forEachOperation(s.api.OpenAPI(), func(op *huma.Operation) { ... })

// Refleksja po interfejsie.
iface := reflect.TypeOf((*orgs.Repository)(nil)).Elem()

// Parsowanie AST, gdy typy tego nie wyrażą.
parser.ParseFile(fset, path, nil, parser.ImportsOnly)
```

Test tego rodzaju musi mieć **komentarz mówiący, jaki błąd wyłapuje**. Bez tego następna osoba zobaczy tylko dziwną
pętlę i usunie ją przy pierwszym refaktorze.

Uważaj na testy puste: jeśli iterujesz po zbiorze, sprawdź, że nie jest pusty.

```go
if iface.NumMethod() == 0 {
t.Fatal("interfejs nie ma metod; ten test przeszedłby pusto")
}
```

## Testy HTTP

Idą przez **pełny router** (`s.http.Handler`), przez `httptest` — bez portu, bez bazy.

```go
func TestListingWidgetsNeedsThePermission(t *testing.T) {
f := newAuthzFixture(t) // konto, organizacja, token

role := f.repo.SeedRole(f.orgID, "readers", string(authz.PermMembersRead))
f.repo.SeedMemberRoles(f.membership, role)

res := f.call(t, http.MethodGet, f.orgPath("/widgets"), "").
expect(t, http.StatusForbidden)

body := decodeProblem(t, res.body)
if body.RequiredPermission != string(authz.PermWidgetsRead) {
t.Errorf("required_permission = %q, want %q", body.RequiredPermission, authz.PermWidgetsRead)
}
}
```

Gotowe narzędzia w pakiecie `api`:

| Pomocnik                           | Do czego                                           |
|------------------------------------|----------------------------------------------------|
| `newAuthzFixture(t, role…)`        | konto + organizacja + token, z opcjonalnymi rolami |
| `f.call(t, metoda, ścieżka, body)` | żądanie z tokenem, wynik z `expect` / `decode`     |
| `f.orgPath("/widgets")`            | ścieżka pod organizacją fikstuury                  |
| `decodeProblem(t, body)`           | dokument błędu z `code` i `required_permission`    |
| `f.repo.Seed*`                     | budowanie stanu autoryzacyjnego                    |

Konfiguracja testowa wyłącza limiter (limity `0`) i używa
`bcrypt.MinCost` — bez tego zestaw testów API spędzałby ~40 s pod `-race` na wyprowadzaniu kluczy, których nic nie
sprawdza.

## Tabele testów rejestrujących operacje

Trzy testy wymagają wpisu dla **każdej** chronionej operacji i przewracają build, gdy go brakuje:

| Test                                   | Plik                             | Czego wymaga                                         |
|----------------------------------------|----------------------------------|------------------------------------------------------|
| `TestTheSnapshotAgreesWithEnforcement` | `httptest/snapshot_http_test.go` | sondy dla operacji organizacyjnej                    |
| `TestSystemScopeIsEnforcedEndToEnd`    | `httptest/platform_http_test.go` | sondy dla operacji platformowej                      |
| `TestEveryMutatingOperationIsAudited`  | `httptest/audit_http_test.go`    | zaklasyfikowania jako mutująca albo tylko do odczytu |

To jest celowe utrudnienie: nowa operacja nie przechodzi, dopóki ktoś nie zdecyduje, jak się zachowuje wobec migawki,
zakresu i dziennika.

## Testy domenowe

Na wspólnym fake'u z `internal/store/repositories/memory`:

```go
func testAuthz(t *testing.T) (*authz.Service, *memory.Authz) {
t.Helper()

repo := memory.NewAuthz(nil)

return authz.NewService(repo), repo
}
```

Fake jest jeden dla całego repozytorium **z premedytacją**: dwa ręcznie pisane stuby tego samego interfejsu się
rozjeżdżają, a wtedy zestaw z luźniejszym przestaje cokolwiek sprawdzać.

Awarię cząstkową wstrzykuje się przez osadzenie fake'a i nadpisanie **jednej**
metody:

```go
type replaceResetErrorRepo struct {
*memory.Users
err error
}

func (r *replaceResetErrorRepo) ReplacePasswordReset(context.Context, *ent.PasswordReset) error {
return r.err
}
```

## Testy na Postgresie

Plik `*_postgres_test.go`, pomijany bez zmiennej:

```go
func TestCosWSQL(t *testing.T) {
db := testDB(t) // t.Skip, gdy POSTGRES_TEST nie jest ustawione
repo := repositories.NewOrgs(db)
...
}
```

```bash
task test:store            # jednorazowa baza na własnym porcie, migracja, testy, sprzątanie
```

Albo ręcznie, przeciwko czemuś, co już stoi:

```bash
task up -- postgres && task migrate
POSTGRES_TEST=1 go test ./internal/store/... -v
```

CI uruchamia je na kontenerze usługowym, więc **nie są opcjonalne** — po prostu lokalnie domyślnie się pomijają.

`POSTGRES_PORT` jest czytane ze środowiska (jak wszystkie pozostałe `POSTGRES_*`) i to nie jest kosmetyka. Dopóki port
był literałem `5432`, na maszynie, gdzie ten port trzyma baza innego projektu, tych testów **nie dało się uruchomić** —
a skierowanie ich tam puściłoby migracje i czyszczenie tabel po cudzych danych. Efektem było to, że pomijały się w
milczeniu i cała warstwa SQL jechała bez weryfikacji.

Dane testowe muszą być unikalne, żeby powtórzone uruchomienie na tej samej bazie nie kolidowało:

```go
Email: "ada+" + uuid.Must(uuid.NewV7()).String() + "@example.com"
```

Co tu należy, a co nie:

| Pisz tutaj                          | Pisz na fake'u                |
|-------------------------------------|-------------------------------|
| warunkowe `UPDATE` i współbieżność  | reguły biznesowe              |
| `SELECT … FOR UPDATE`               | walidacja                     |
| rzutowania (`::inet`), `NULLS LAST` | mapowanie błędów              |
| kaskady i ograniczenia unikalności  | wszystko, co nie dotyka SQL-a |
| transakcyjność (cofnięcie zmiany)   |                               |

## Zestaw kontraktowy: jedna tabela przypadków, dwie implementacje

`internal/store/repositories/contract/` uruchamia **te same** przypadki na fake'u i na Postgresie:

```go
eachBackend(t, func(t *testing.T, b *backend) {
    // wszystko przez interfejsy: b.repo, b.dir, b.perms
})
```

Powód jest konkretny, nie estetyczny. Fake odtwarza semantykę SQL-a ręcznie i **rozjechał się**: członkostwo, którego
konto usunięto, było członkiem dla jednego i nie było dla drugiego, reguła ostatniego właściciela liczyła je inaczej w
każdym — a ponieważ testy HTTP jeżdżą wyłącznie na fake'u, wszystko przechodziło, gdy Postgres zachowywał się inaczej.
Naprawianie tego po jednym naprawia egzemplarze; zestaw kontraktowy zamyka **klasę**.

Co tu należy: wszystko, co jest **odpowiedzią na to samo pytanie** i musi wypaść identycznie — rozstrzyganie
członkostwa, zakres najemcy, cykl życia zaproszenia, fakty, które trafiają do strażników domenowych.

Czego tu nie ma: fikstury. Utworzenie konta albo miękkie usunięcie go nie da się wyrazić przez `orgs.Repository`, a obie
implementacje robią to zupełnie inaczej — dlatego `backend` wystawia garść funkcji do budowania stanu. **Wszystko, na
czym przypadek asercjonuje, sięga przez interfejsy**, inaczej testowałby fikstury.

Połowa na fake'u jedzie wszędzie; połowa Postgresowa pomija się bez `POSTGRES_TEST`.

## Nazewnictwo

Nazwa testu mówi, **jaka własność jest sprawdzana**, nie jaka metoda jest wołana:

```
TestTheLastOwnerCannotBeDemoted
TestASuspendedAccountCannotGetAFreshToken
TestAnAuditRowRollsBackWithItsChange
```

zamiast `TestSetMemberRoles`, `TestSignIn`, `TestReplaceMemberRoles`.

## Sprawdź, że test potrafi nie przejść

Przy teście strukturalnym warto raz go zepsuć celowo — usunąć wpis, zdublować klasyfikację — i zobaczyć komunikat. Test
przechodzący pusto wygląda dokładnie tak samo jak działający.
