# Repozytoria

## Interfejs należy do konsumenta

Interfejs deklaruje **domena**, implementuje **store**. Dzięki temu store zależy od domeny, nigdy odwrotnie, a interfejs
wymienia tylko to, czego domena naprawdę używa.

```
internal/domain/widgets/repository.go       ← interfejs + błędy domenowe
internal/store/repositories/widgets.go      ← implementacja GORM
internal/store/repositories/memory/…        ← fake do testów
```

## Lista kontrolna

1. Interfejs i błędy w `internal/domain/<rzecz>/repository.go`
2. Implementacja GORM z asercją `var _ <rzecz>.Repository = (*T)(nil)`
3. **Ten sam interfejs w fake'u** in-memory
4. Mapowanie błędów w `problem`
5. Test na fake'u; test na Postgresie tylko dla tego, co fake udaje w Go

Krok 3 nie jest opcjonalny. Dwa ręcznie pisane stuby dwudziestometodowego interfejsu się rozjeżdżają, a wtedy zestaw
testów z luźniejszym przestaje cokolwiek sprawdzać.

## Interfejs

```go
// Repository to trwałość, której potrzebuje ten pakiet.
//
// Każda metoda przyjmuje identyfikator organizacji jako **drugi parametr**.
// To nie jest reguła formatowania: dzięki temu nie da się pobrać wiersza bez
// wskazania organizacji, więc pominięcie kontroli zasięgu jest błędem
// kompilacji, a nie dziurą do wypatrzenia na review.
type Repository interface {
// Widget zwraca ErrNotFound, gdy widget należy do innej organizacji.
Widget(ctx context.Context, orgID, widgetID uuid.UUID) (*models.Widget, error)

Widgets(ctx context.Context, orgID uuid.UUID, limit, offset int) ([]models.Widget, error)

CreateWidget(ctx context.Context, orgID uuid.UUID, widget *models.Widget) error
}
```

Konwencje:

- `ctx` zawsze pierwszy, `orgID` zawsze drugi,
- `time.Time` przekazywany **do środka**, nie odczytywany w repozytorium — dzięki temu test steruje czasem,
- „nie znaleziono" to zawsze błąd domenowy, nigdy `gorm.ErrRecordNotFound`,
- komentarz przy metodzie mówi, **kiedy** zwraca który błąd.

`TestScopedRepositoryMethodsTakeAnOrganization` sprawdza refleksją, że drugi parametr faktycznie jest `uuid.UUID`.

Rzeczy niezwiązane z pojedynczą organizacją (tworzenie organizacji, listowanie kont) idą do **osobnego** interfejsu —
`Provisioner`, `Directory` — żeby zasada „każda metoda tutaj jest zawężona" nie miała gwiazdek.

### Grupy w jednym interfejsie

Kiedy interfejs przekracza kilkanaście metod, dziel go na **grupy i składaj z powrotem przez zagnieżdżenie**, zamiast
zostawiać jedną listę albo rozdawać konsumentom cztery pola:

```go
type Repository interface {
Organizations
Memberships
Roles
Invitations
}
```

Dlaczego tak:

- `orgs.Service` używa wszystkich czterech grup, więc jedno pole `repo Repository` jest uczciwsze niż cztery pola, które
  zawsze dostają ten sam obiekt,
- konsument, który potrzebuje tylko listy członków, może zażądać `orgs.Memberships` — i **nie ma jak** sięgnąć po rolę,
- podwójna implementacja w teście jest wtedy odpowiednio mniejsza (`cmd/bootstrap` bierze sam `Provisioner`),
- refleksja widzi metody promowane, więc `TestScopedRepositoryMethodsTakeAnOrganization` obejmuje wszystkie grupy —
  także tę dodaną później, bez dopisywania jej do testu.

**Pułapka, która już się zdarzyła.** Trzy metody zaproszeń (`InvitationsForOrganization`, `ReissueInvitation`,
`WithdrawInvitation`) przyjmują `orgID`, ale przez kilka commitów leżały w `Directory` — bo tam dopisywano wtedy kod
zaproszeń. `Directory` jest z definicji niezawężony i test refleksyjny go nie czyta, więc były to jedyne zawężone metody
w pakiecie, których nic nie pilnowało. Jeśli metoda przyjmuje `orgID`, jej miejsce jest w grupie wewnątrz `Repository`,
a nie w `Directory` — niezależnie od tego, obok czego wygodnie ją pisać.

## Paginacja

Każda metoda, która zwraca listę, przyjmuje `limit` i `offset`. Limit **przycina
warstwa domenowa** — `orgs.MaxMemberPage`, `orgs.MaxRolePage`,
`user.MaxUserPage` — a nie repozytorium i nie handler:

- schemat huma (`PageInput`) odrzuca zbyt duży limit na wejściu, żeby klient
  dostał czytelny błąd 422,
- `Service` przycina go jeszcze raz, żeby reguła nie zależała od tego, czy
  wywołanie przyszło z HTTP.

Repozytorium nie interpretuje limitu zero jako „wszystko". Zwraca pustą stronę.
Odpowiedź na cichą pomyłkę ma być widoczna, a nie polegać na wysłaniu całej
tabeli — po to jest paginacja.

**Porządek musi być totalny.** `ORDER BY u.name` z `LIMIT`/`OFFSET` nie wystarczy:
nazwiska się powtarzają, a sortowanie z remisami może zwrócić remisujące wiersze
w dowolnej kolejności przy każdym zapytaniu. Wtedy jeden wiersz trafia na dwie
strony, a inny na żadną. Dlatego listowanie członków sortuje po `(name, id)` —
identyfikator jest rozstrzygający — a listowanie roli po `(is_system, key)`, gdzie
`key` jest już unikalny w organizacji.

To samo dotyczy atrapy w `memory`: jeśli sortuje inaczej niż SQL, po stronicowaniu
różnica przestaje być kosmetyczna i zmienia to, **które** wiersze są na stronie.
Oba porządki są przypięte w `internal/store/repositories/contract`.

## Implementacja GORM

```go
type Widgets struct {
db *store.DB
}

func NewWidgets(db *store.DB) *Widgets { return &Widgets{db: db} }

// Asercja kompilacji. Bez niej rozjechana sygnatura wyszłaby dopiero
// w main, daleko od obu definicji.
var _ widgets.Repository = (*Widgets)(nil)

func (r *Widgets) Widget(ctx context.Context, orgID, widgetID uuid.UUID) (*models.Widget, error) {
var widget models.Widget

err := r.db.WithContext(ctx).
First(&widget, "id = ? AND organization_id = ?", widgetID, orgID).Error
if err != nil {
return nil, translateWidgetError("widget", err)
}

return &widget, nil
}
```

Zawężenie po `organization_id` jest w `WHERE`, nie w `if` po odczycie. Wiersz z cudzej organizacji **nie zostaje
znaleziony**, zamiast zostać znaleziony i odrzucony.

## Tłumaczenie błędów

Tu kończy się GORM. Wzorzec:

```go
func translateWidgetError(op string, err error) error {
switch {
case err == nil:
return nil
case errors.Is(err, gorm.ErrRecordNotFound):
return widgets.ErrNotFound
case errors.Is(err, gorm.ErrDuplicatedKey):
return widgets.ErrKeyTaken
case errors.Is(err, models.ErrWidgetLocked):
return err // błąd modelu przechodzi dalej
default:
return fmt.Errorf("store: %s: %w", op, err)
}
}
```

`gorm.ErrDuplicatedKey` pojawia się dzięki `TranslateError: true` w konfiguracji połączenia. Bez tego dostaniesz surowy
błąd pgx.

Nieprzetłumaczony błąd dojdzie do `problem` i stanie się nieprzejrzystym `500` — bezpiecznie, ale bezużytecznie dla
klienta.

## Fake in-memory

`internal/store/repositories/memory` niesie tę samą asercję i te same semantyki:

```go
var _ widgets.Repository = (*Widgets)(nil)
```

Zasady, które trzeba skopiować **dokładnie**, bo inaczej test przejdzie na fake'u i padnie na Postgresie:

| Zasada                                               | Dlaczego                                                                        |
|------------------------------------------------------|---------------------------------------------------------------------------------|
| jeden `sync.Mutex`, `Lock`/`defer Unlock` na wejściu |                                                                                 |
| zapis i odczyt **kopiują** strukturę                 | test nie może mutować stanu przez zwrócony wskaźnik                             |
| ten sam błąd w tych samych sytuacjach                | zwłaszcza „nie znaleziono" kontra „nie twoje"                                   |
| sortowanie jak w SQL-u                               | iteracja po mapie jest losowa; test na pierwszym elemencie przechodziłby losowo |
| reguły z modelu wołane, nie przepisywane             | `device.Revoke()`, a nie własna kopia warunku                                   |

Metody spoza interfejsu, potrzebne testom do zbudowania stanu, mają prefiks
`Seed` — np. `SeedOrganization`, `SeedMember`. Trzymanie ich w jednym miejscu jest tańsze niż trzy kopie tego samego
setupu w trzech pakietach testowych.

## Transakcje

Nie ma `WithTx` ani abstrakcji jednostki pracy. Atomowość jest własnością **jednej metody repozytorium**, nie czymś, co
składa domena:

```go
err := r.db.WithContext(ctx).Transaction(func (tx *gorm.DB) error {
if err := tx.Create(widget).Error; err != nil {
return err
}

return record(ctx, tx, &models.AuthzEvent{ ... })
})
```

Szczegóły i pułapki: [praca z bazą](005_database_access.md).

## Testy

Domyślnie testuj przez fake — szybko i bez bazy. Test na Postgresie pisz **tylko** dla tego, co fake udaje w Go:
warunkowe `UPDATE`, `FOR UPDATE`, rzutowania typu `::inet`, `NULLS LAST`, kaskady, transakcyjność.

Reguła z `user_postgres_test.go`: jeśli test przeszedłby na fake'u, jego miejsce jest na fake'u.
