# Tłumaczenia

Katalogi są wkompilowane w binarkę (`go:embed`) i mieszkają w
`internal/i18n/locales/<język>.json`. Języki: `en` (fallback) i `pl`.

Dlaczego akurat tak, a nie w bazie: [Błędy i języki](../design/008_errors_and_i18n.md#języki).

## Nowy komunikat błędu

Trzy miejsca, zawsze razem:

```go
// internal/api/problem/document.go
const CodeWidgetLocked = "widget_locked"

// internal/api/problem/errors.go
case errors.Is(err, widgets.ErrWidgetLocked):
return newDocument(locale, http.StatusConflict, CodeWidgetLocked)
```

```json
// internal/i18n/locales/en.json
"error.widget_locked": "the widget is locked",

// internal/i18n/locales/pl.json
"error.widget_locked": "widget jest zablokowany",
```

Klucz to zawsze `error.<code>`. Brak w którymkolwiek języku przewraca
`TestEveryLocaleDefinesTheSameKeys`.

## Interpolacja

Komunikat może mieć czasowniki formatu:

```json
"error.method_not_allowed": "metoda %s nie jest dozwolona dla %s"
```

Argumenty przekazuje się przy zapisie:

```go
problem.Write(w, r, http.StatusMethodNotAllowed, problem.CodeMethodNotAllowed, r.Method, r.URL.Path)
```

Uwaga na kolejność argumentów w językach o innym szyku — jeśli kiedyś stanie się to problemem, `%[1]s` pozwala je
przestawić bez zmiany kodu.

## Nowy język

1. Skopiuj `en.json` do `<kod>.json`.
2. **Przetłumacz wszystko.** Katalog będący kopią `en` przechodzi testy kompletności; `TestPolishIsActuallyTranslated`
   istnieje właśnie po to i warto dopisać jego odpowiednik.
3. `go test ./internal/i18n/` — powie dokładnie, których kluczy brakuje.

Nic więcej nie trzeba: lista języków wynika z plików w katalogu, a matcher
`x/text` sam obsłuży zawężenie `pl-PL` → `pl`.

## Co jest tłumaczone, a co nie

| Element                            | Skąd tekst                                                              |
|------------------------------------|-------------------------------------------------------------------------|
| komunikat błędu (`detail`)         | katalog, klucz `error.<code>`                                           |
| nazwa i opis uprawnienia           | katalog, `permission.<klucz>.name` / `.description`                     |
| nazwa i opis roli systemowej       | katalog, `role.<klucz>.name` / `.description`                           |
| nazwa roli własnej                 | kolumna `roles.name` — **nietłumaczona**, w języku, w którym ją wpisano |
| `code`, `required_permission`      | **nigdy** — to identyfikatory maszynowe                                 |
| `Summary` / `Description` operacji | angielski, w kodzie; trafiają do OpenAPI                                |

Ostatni wiersz jest świadomy: kontrakt API i jego dokumentacja są po angielsku, tak jak kod. Tłumaczone są komunikaty
kierowane do użytkownika końcowego.

## Czego nie tłumaczyć

`code` musi zostać stabilny między językami i wydaniami — po nim klient rozgałęzia logikę. Klient, który przełącza się
na `detail`, zepsuje się przy pierwszej korekcie stylistycznej.

## Wybór języka

```
User.Locale  →  Accept-Language  →  en
```

`User.Locale` zapisuje się **tylko wtedy**, gdy klient faktycznie o jakiś język poprosił przy rejestracji. Do tego
rozróżnienia służy `Catalog.Match` (zwraca też informację, czy w ogóle poproszono), w odróżnieniu od
`Catalog.Negotiate`, które zawsze odpowiada, bo odpowiedź trzeba w czymś napisać.

Nie ma jeszcze endpointu zmieniającego język po rejestracji.
