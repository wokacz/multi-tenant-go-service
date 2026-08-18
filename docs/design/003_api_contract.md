# Kontrakt API

## Wersjonowanie od pierwszej trasy

`/v1` występuje w ścieżce **i** w drzewie katalogów (`internal/api/v1`), więc v2 będzie pakietem obok, a nie
przepisaniem tego, co jest.

Endpointy operacyjne stoją **poza** wersją: `/health`, `/docs`,
`/openapi.json`. Nie są częścią kontraktu, a sonda nie powinna wymagać przekonfigurowania przy zmianie wersji API.

## OpenAPI jest generowane, nie pisane

huma wyprowadza schemat każdej operacji z typów wejścia i wyjścia w Go, więc specyfikacja nie może rozjechać się z
kodem.

Dokument jest przy tym **commitowany** w [`api/openapi.yaml`](../../api/openapi.yaml). Generowany-i-commitowany to
sedno: kontrakt pozostaje code-first, a jego zmiana i tak pojawia się jako diff do recenzji w pull requeście, zamiast
być niewidocznym skutkiem ubocznym edycji handlera.

```bash
task openapi        # przegeneruj
task openapi:check  # CI: nieaktualny plik = błąd
```

Dokument renderuje się z **ustalonej** konfiguracji, nie z działającej, więc nie zmienia się w zależności od portu,
którego akurat użył programista.

W produkcji `/docs`, `/openapi.json` i `/schemas` **nie są serwowane**. Kontrakt to plik w repozytorium; proces nie
publikuje mapy samego siebie.

## Co trzeba wypełnić przy operacji

| Pole                             | Dlaczego                                                    |
|----------------------------------|-------------------------------------------------------------|
| `OperationID`                    | klucz w mapach autoryzacji i nazwa w generowanych klientach |
| `Summary`, `Description`, `Tags` | trafiają wprost do dokumentacji i klientów                  |
| `Security`                       | musi być, jeśli operacja nie jest publiczna                 |
| `Errors: []int{...}`             | **każdy** status, jaki handler potrafi zwrócić              |
| `DefaultStatus`                  | dla 201/202/204                                             |

Status pominięty w `Errors` nie trafia do specyfikacji ani do generowanych klientów, mimo że handler go zwraca — klient
napisany ze specyfikacji nie ma wtedy gałęzi na odmowę. Pilnują tego testy kontraktowe, patrz
[Autoryzacja](007_authorization.md).

Przepis na dodanie operacji:
[instrukcja nowego endpointu](../guides/002_add_endpoint.md).

## DTO nie opuszczają `internal/api`

Modele **nigdy** nie trafiają do JSON-a. `ent.User` niesie `PasswordHash`,
`IsProtected`, `DeletedAt` i `SuspendedAt`; zakodowanie go wprost ujawniłoby pierwsze i wystawiło wewnętrzny cykl życia
w pozostałych.

Typy wysyłkowe i funkcje `newXxxResponse` mieszkają w pakiecie `v1`. Przejście przez DTO oznacza też, że **dodanie
kolumny nie może po cichu poszerzyć API**.

Reguła ma jeszcze jedno zastosowanie: administracyjny widok konta (`PlatformUserResponse`) jest osobnym typem od
`UserResponse`, żeby pole dodane dla administratora nie wyciekło na `/v1/me`.

## Ograniczenia w tagach struktur

huma czyta walidację ze znaczników pól:

```go
Password string `json:"password" minLength:"12" maxLength:"72" doc:"..."`
```

Znacznik nie może odwołać się do stałej, więc reguła domenowa i udokumentowany limit są związane **asercją kompilacji**:

```go
const (
	_ = uint(user.MinPasswordLength - minPasswordLength)
	_ = uint(minPasswordLength - user.MinPasswordLength)
)
```

Gdy obie wartości się rozjadą, jedno z odejmowań przekręci się pod zerem i pakiet przestanie się kompilować. Alternatywą
jest specyfikacja obiecująca limit, którego serwis nie egzekwuje.

## Odpowiedzi błędne

Każdy błąd to `application/problem+json` (RFC 7807) — łącznie z tymi, które generuje router zanim huma zobaczy żądanie,
i z odpowiedzią po panice.

```json
{
  "status": 403,
  "detail": "brak uprawnień: wymagane jest Podgląd ról",
  "code": "forbidden_requires",
  "required_permission": "roles.read"
}
```

`code` jest stabilny między językami i wydaniami — to po nim klient rozgałęzia logikę. `detail` jest dla ludzi i zmienia
się z językiem; **klient nigdy go nie parsuje**. Szczegóły: [Błędy i języki](008_errors_and_i18n.md).

## Listy zawsze są tablicami

Kolekcje buduje się przez `make([]T, 0, n)`, żeby pusty wynik serializował się jako `[]`, a nie `null`. Klient mapujący
po wyniku nie powinien obsługiwać przypadku pustego osobno.

## Przegląd endpointów

Aktualna lista operacji jest w [`api/openapi.yaml`](../../api/openapi.yaml) i w Swagger UI pod `/docs` w developmencie.
Nie powielamy jej tutaj — tabela w dokumentacji dezaktualizuje się przy pierwszej trasie, o której ktoś zapomni.

Grupy:

| Prefiks                                            | Zakres                                                                                       |
|----------------------------------------------------|----------------------------------------------------------------------------------------------|
| `/health`                                          | sonda; sięga do bazy, bo instancja bez Postgresa nie obsłuży niczego                         |
| `/v1/users`, `/v1/sessions`, `/v1/password-resets` | rejestracja, logowanie, reset hasła                                                          |
| `/v1/me/*`                                         | własne konto: profil, urządzenia, zaproszenia, historia logowań, drugi składnik, uprawnienia |
| `/v1/permissions`                                  | katalog uprawnień produktu                                                                   |
| `/v1/orgs/{orgID}/*`                               | organizacja: członkowie, role, dziennik zmian                                                |
| `/v1/platform/*`                                   | cała instalacja: organizacje, konta, dziennik                                                |
