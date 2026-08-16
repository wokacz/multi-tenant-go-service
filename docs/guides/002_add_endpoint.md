# Dodanie endpointu

Najczęstsza czynność w projekcie i ta, w której najłatwiej o pominięcie kroku. Poniższa lista jest kompletna — nic poza
nią nie jest wymagane.

## Lista kontrolna

1. DTO wejścia i wyjścia w `internal/api/v1/<moduł>.go`
2. Rejestracja operacji przez `huma.Register`
3. **Klasyfikacja autoryzacyjna** w `internal/api/authz.go`
4. Handler
5. Mapowanie nowych błędów w `internal/api/problem/errors.go`
6. `task openapi` i commit `api/openapi.yaml`
7. Test

Kroki 3 i 6 są tymi, o których się zapomina — i oba przewracają build, więc zapomnienie kosztuje minutę, nie incydent.

## 1. DTO

Typy wysyłkowe nigdy nie są modelami. Konwersja mieszka w jednej funkcji
`newXxxResponse`.

```go
type ListWidgetsInput struct {
// Deklarowane wyłącznie po to, żeby huma udokumentowała parametr.
// Handler go NIE czyta — organizacja pochodzi z grantu.
OrgID uuid.UUID `path:"orgID" format:"uuid" doc:"Identyfikator organizacji"`

Limit int `query:"limit" minimum:"1" maximum:"100" default:"100" doc:"Ile zwrócić"`
}

type ListWidgetsOutput struct {
Body struct {
Widgets []WidgetResponse `json:"widgets"`
}
}
```

Walidacja idzie w tagach (`minLength`, `maximum`, `format`, `enum`). Jeśli limit istnieje też w domenie, zwiąż obie
wartości asercją kompilacji — wzorzec w
`internal/api/v1/dto.go`.

## 2. Rejestracja

```go
huma.Register(api, huma.Operation{
OperationID: "list-widgets", // klucz w mapach autoryzacji
Method:      http.MethodGet,
Path:        Prefix + "/orgs/{orgID}/widgets",
Summary:     "Lista widgetów",
Description: "Wymaga widgets.read. Wołający bez członkostwa dostaje 404.",
Tags:        []string{"organizations"},
Security:    bearer(),
Errors:      orgErrors(), // 401, 403, 404
}, h.list)
```

Pomocniki `bearer()`, `orgErrors()` i `platformErrors()` są w
`internal/api/v1/router.go`. Używaj ich zamiast wpisywać literały — literał łatwo przepisać tak, że nadal się kompiluje
i niczego nie dokumentuje.

`Errors` musi wymieniać **każdy** status, jaki handler potrafi zwrócić. Status pominięty tam nie istnieje w kontrakcie
ani w generowanych klientach.

Nie zapomnij wywołać `registerWidgets` z `v1.Register`.

## 3. Klasyfikacja autoryzacyjna

Operacja musi trafić do **dokładnie jednego** zbioru w `internal/api/authz.go`:

```go
// bez tokenu — internal/api/middleware.go
var publicOperations = map[string]bool{ "health": true, ... }

// token wystarcza, bo działa na własnym koncie
var selfServiceOperations = map[string]bool{ "get-me": true, ... }

// wymaga uprawnienia
var operationAccess = map[string]accessRule{
"list-widgets": {authz.PermWidgetsRead, authz.ScopeOrganization},
}
```

Pominięcie kończy się `403` i wpisem w logu — i przewraca
`TestEveryOperationHasExactlyOneAuthorizationRule`.

Reguła `ScopeOrganization` wymaga `{orgID}` w ścieżce, `ScopeSystem` wymaga jego braku. Pilnuje tego
`TestOrgScopedRulesLiveOnOrgScopedPaths`.

## 4. Handler

```go
func (h *widgetHandlers) list(ctx context.Context, in *ListWidgetsInput) (*ListWidgetsOutput, error) {
grant, err := grantFrom(ctx) // organizacja i uprawnienia wołającego
if err != nil {
return nil, err
}

widgets, err := h.widgets.List(ctx, grant, in.Limit)
if err != nil {
return nil, problem.Error(ctx, err)
}

out := &ListWidgetsOutput{}
out.Body.Widgets = make([]WidgetResponse, 0, len(widgets))   // [] zamiast null

for i := range widgets {
out.Body.Widgets = append(out.Body.Widgets, newWidgetResponse(&widgets[i]))
}

return out, nil
}
```

Zasady:

- **organizacja pochodzi z `grantFrom(ctx)`**, nigdy z `in.OrgID` —
  `TestHandlersDoNotReadTheOrgIDParameter` przewraca build za odczyt pola;
- każdy błąd wychodzi przez `problem.Error(ctx, err)`;
- kolekcje przez `make([]T, 0, n)`;
- handler `204` ma sygnaturę `(*struct{}, error)` i zwraca `nil, nil`;
- tożsamość wołającego przy operacjach samoobsługowych: `auth.SessionFrom(ctx)`.

## 5. Błędy

Nowy błąd domenowy potrzebuje trzech rzeczy:

```go
// internal/domain/widgets/repository.go
var ErrWidgetLocked = errors.New("widgets: widget is locked")

// internal/api/problem/document.go
const CodeWidgetLocked = "widget_locked"

// internal/api/problem/errors.go
case errors.Is(err, widgets.ErrWidgetLocked):
return newDocument(locale, http.StatusConflict, CodeWidgetLocked)
```

Plus tłumaczenia `error.widget_locked` w **każdym** pliku
`internal/i18n/locales/*.json` — inaczej `TestEveryLocaleDefinesTheSameKeys`
przewraca build.

Niezmapowany błąd staje się `500` z wpisem w logu. To jest bezpieczne domyślne, ale dla znanej sytuacji biznesowej to
zły interfejs.

## 6. Kontrakt

```bash
task openapi
git add api/openapi.yaml
```

CI porównuje wygenerowany plik z commitowanym.

## 7. Test

Minimum: odmowa i przejście, przez pełny router.

```go
func TestListingWidgetsNeedsThePermission(t *testing.T) {
f := newAuthzFixture(t) // konto bez ról

res := f.call(t, http.MethodGet, f.orgPath("/widgets"), "").
expect(t, http.StatusForbidden)

body := decodeProblem(t, res.body)
if body.RequiredPermission != string(authz.PermWidgetsRead) {
t.Errorf("required_permission = %q, want %q", body.RequiredPermission, authz.PermWidgetsRead)
}
}
```

Jeśli operacja **zmienia** stan, dopisz ją do tabel w
`internal/api/audit_http_test.go` oraz `snapshot_http_test.go` — oba testy przewracają build, gdy chroniona operacja nie
ma tam wpisu.

Więcej: [instrukcja testów](007_write_tests.md).

## Operacja platformowa

Różnice wobec organizacyjnej:

|               | Organizacyjna               | Platformowa                     |
|---------------|-----------------------------|---------------------------------|
| Ścieżka       | `/v1/orgs/{orgID}/…`        | `/v1/platform/…`                |
| Zakres reguły | `authz.ScopeOrganization`   | `authz.ScopeSystem`             |
| Błędy         | `orgErrors()` (401/403/404) | `platformErrors()` (401/403)    |
| Organizacja   | z grantu                    | z ciała lub ścieżki jako `{id}` |

Brak `404` po stronie platformowej jest celowy: wołający jest już ustalonym administratorem instalacji, więc nie ma
przed nim czego ukrywać.
