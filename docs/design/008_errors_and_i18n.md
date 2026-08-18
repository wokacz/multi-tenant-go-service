# Błędy i języki

Dwa tematy w jednym dokumencie, bo są tym samym mechanizmem oglądanym z dwóch stron: odpowiedź błędna niesie **kod** dla
maszyny i **tekst** dla człowieka, a tekst zależy od języka.

## Dokument problemu

Każda odpowiedź błędna to `application/problem+json` (RFC 7807) z dwoma polami ponad standard:

```json
{
  "status": 403,
  "detail": "brak uprawnień: wymagane jest Podgląd ról",
  "code": "forbidden_requires",
  "required_permission": "roles.read"
}
```

| Pole                  | Kto z niego korzysta                                                                                            |
|-----------------------|-----------------------------------------------------------------------------------------------------------------|
| `code`                | **klient** — stabilny między językami i wydaniami, po nim rozgałęzia logikę i pod niego podstawia własne teksty |
| `detail`              | **człowiek** — zmienia się z językiem, może zostać przeredagowany w każdej chwili; klient nigdy go nie parsuje  |
| `required_permission` | klient — surowy klucz, porównywalny z katalogiem z `GET /v1/permissions`                                        |
| `errors[]`            | klient — błędy walidacji per pole                                                                               |

Kody są stałymi w `internal/api/problem/document.go` (`problem.CodeForbidden`
i podobne), więc test i handler odwołują się do tej samej wartości.

## Trzy miejsca, w których powstaje odpowiedź błędna

| Skąd                               | Czym                                              | Tłumaczy                      |
|------------------------------------|---------------------------------------------------|-------------------------------|
| handler                            | `problem.Error(ctx, err)`                         | tak — ma kontekst             |
| middleware huma                    | `huma.WriteErr(...)` → `huma.NewErrorWithContext` | tak — dostaje `huma.Context`  |
| router chi (404, 405, 429, panika) | `problem.Write(w, r, ...)`                        | tak — dostaje `*http.Request` |

Wszystkie trzy przechodzą przez `newDocument`, więc kształt odpowiedzi jest jeden.

### Dlaczego podmieniamy globalną `huma.NewError`

huma odbija schemat odpowiedzi błędnych z tego, co zwraca **globalna zmienna**
`huma.NewError` (`defineErrors` w `huma.go`). Bez podmiany `code` i
`required_permission` byłyby w odpowiedziach i nieobecne w kontrakcie, a każdy generowany klient by je gubił.

`problem.Install()` robi tę podmianę **raz**, przed rejestracją tras — po rejestracji byłoby za późno. Nowy typ musi
implementować
`huma.ContentTypeFilter` zwracający `application/problem+json`, inaczej wszystkie odpowiedzi błędne zmieniłyby typ
zawartości.

`TestTheProblemSchemaCarriesTheExtraFields` pilnuje kolejności.

## Mapa błędów domenowych

`problem.Error` to jedyne miejsce, w którym błąd domenowy staje się statusem.

| Błąd domenowy                                                                              | Status | `code`                                          |
|--------------------------------------------------------------------------------------------|--------|-------------------------------------------------|
| `user.ErrNotFound`, `orgs.ErrNotFound`, `authz.ErrNotMember`                               | 404    | `not_found`                                     |
| `user.ErrUnauthorized`                                                                     | 401    | `unauthorized`                                  |
| `user.ErrInvalidCredentials`                                                               | 401    | `invalid_credentials`                           |
| `user.ErrInvalidResetCode`, `ErrInvalidTwoFactorCode`                                      | 401    | `invalid_reset_code`, `invalid_two_factor_code` |
| `user.ErrDeviceRevoked`                                                                    | 403    | `device_revoked`                                |
| `user.ErrSuspended`                                                                        | 403    | `account_suspended`                             |
| `authz.ErrForbidden`                                                                       | 403    | `forbidden`                                     |
| `authz.ErrPrivilegeEscalation`                                                             | 403    | `privilege_escalation`                          |
| `orgs.ErrRoleProtected`                                                                    | 403    | `role_protected`                                |
| `authz.ErrInsufficientRank`                                                                 | 403    | `insufficient_rank`                             |
| `orgs.ErrCannotRevokeOwnLastSystemRole`                                                      | 409    | `last_system_role`                              |
| `orgs.ErrInvalidSystemRole`                                                                 | 422    | `invalid_system_role`                           |
| `user.ErrInvalidEmailCode`                                                                  | 401    | `invalid_email_code`                            |
| `user.ErrEmailTaken`                                                                        | 409    | `email_taken` (tylko przy zmianie adresu)       |
| `user.ErrSameEmail`, `ErrEmailInvalid`                                                      | 422    | `same_email`, `invalid_email`                   |
| `user.ErrLocaleUnsupported`                                                                 | 422    | `unsupported_locale`                            |
| `authz.ErrUnknownPermission`, `ErrWrongScope`                                               | 422    | `unknown_permission`, `wrong_scope`             |
| `orgs.ErrLastOwner`, `ErrRoleInUse`, `ErrRoleKeyTaken`, `ErrAlreadyMember`, `ErrSlugTaken` | 409    | odpowiedni kod                                  |
| `models.ErrProtected`                                                                      | 409    | `record_protected`                              |
| błędy walidacji domenowej                                                                  | 422    | odpowiedni kod                                  |
| `context.Canceled`                                                                         | 499    | `client_closed`                                 |
| `context.DeadlineExceeded`                                                                 | 504    | `timeout`                                       |
| **cokolwiek innego**                                                                       | 500    | `internal`                                      |

Ostatni wiersz jest najważniejszy. Niezmapowany błąd trafia do logu przy identyfikatorze żądania, a klient dostaje
nieprzejrzyste `500` — surowe błędy niosą nazwy tabel, fragmenty zapytań, a czasem poświadczenia.

Zauważ, czego na tej liście nie ma: **GORM-a**. Repozytoria tłumaczą błędy sterownika na domenowe, więc `problem` mapuje
wyłącznie słownictwo domeny. Błąd sterownika, który tu dotarł, oznacza, że jakieś repozytorium zapomniało go
przetłumaczyć — i słusznie staje się nieprzejrzystym `500`.

Dodanie mapowania: [instrukcja nowego endpointu](../guides/002_add_endpoint.md#5-błędy).

## Języki

Model hybrydowy:

| Co                      | Gdzie                                                         | Dlaczego                                        |
|-------------------------|---------------------------------------------------------------|-------------------------------------------------|
| komunikaty błędów       | katalog w kodzie (`internal/i18n/locales/*.json`, `go:embed`) | zmieniają się razem z kodem i przechodzą review |
| nazwy i opisy uprawnień | katalog w kodzie                                              | uprawnienie *jest* kodem                        |
| nazwy ról systemowych   | katalog w kodzie                                              | powstają z katalogu                             |
| nazwy ról własnych      | tabela `role_translations`                                    | powstają w runtime — **jeszcze niepodłączone**  |

Języki: `en` (fallback) i `pl`.

### Pierwszeństwo

```
User.Locale  →  Accept-Language  →  en
```

`User.Locale` jest zapamiętywany **tylko wtedy, gdy klient faktycznie o jakiś język poprosił** przy rejestracji.
Zapisanie fallbacku dla kogoś, kto nie wyraził wyboru, zamienia zgadnięcie w trwałą decyzję i na zawsze wyłącza
negocjację per żądanie. `Catalog.Match` odróżnia „nie poprosił" od „poprosił o coś, czego nie mamy"; `Catalog.Negotiate`
zawsze odpowiada, bo odpowiedź trzeba w czymś napisać.

**Trzecia funkcja: `Catalog.Resolve`** — dla języka **wybranego świadomie**, przez `PATCH /v1/me`. Jak `Match` zgłasza
brak zamiast schodzić do fallbacku: zapamiętanie angielskiego dla kogoś, kto poprosił o niemiecki, dałoby mu na stałe
język, o który nigdy nie prosił, więc taka prośba kończy się `422 unsupported_locale`. `Resolve` **normalizuje** też tag —
`pl-PL` zapisuje się jako `pl`, żeby w kolumnie była jedna pisownia na język, a nie tyle, ile przeglądarek.

Puste `locale` w `PATCH /v1/me` to wartość znacząca: „nie mam preferencji, negocjuj per żądanie". Dlatego pola żądania są
wskaźnikami — bez tego nie da się odróżnić „nie wspominam o tym polu" od „ustaw je na puste", a raz wybranego języka nie
dałoby się oddać przeglądarce.

Katalog żyje **przy krawędzi**, nie w `internal/domain`: rejestracja rozstrzyga język z nagłówka w handlerze i zmiana
profilu robi to samo. Domena dostaje już rozstrzygnięty tag, a `user.ErrLocaleUnsupported` jest tylko słownikiem, w
którym handler zgłasza odmowę.

Parsowanie `Accept-Language` idzie przez `golang.org/x/text/language`. Ręczny parser myli wagi `q` i nie wie, że `pl-PL`
ma trafić do katalogu `pl`.

### Nagłówki

Każda odpowiedź niesie `Content-Language`. `Vary: Accept-Language` chroni przed cache'em pośredniczącym, który podałby
polski komunikat angielskiemu klientowi.

### Klucze

```
error.<code>                        error.forbidden_requires
permission.<klucz>.name             permission.roles.read.name
permission.<klucz>.description
role.<klucz>.name                   role.owner.name
role.<klucz>.description
```

Brakujący klucz zwraca **sam klucz**, nie pusty tekst — widać go wtedy w odpowiedzi i da się go wygrepować. W praktyce
nie powinno do tego dojść, bo kompletność pilnują testy:

| Test                                            | Co wyłapuje                                                |
|-------------------------------------------------|------------------------------------------------------------|
| `TestEveryPermissionIsTranslatedInEveryLocale`  | uprawnienie bez nazwy lub opisu w którymś języku           |
| `TestEveryShippedRoleIsTranslatedInEveryLocale` | to samo dla ról                                            |
| `TestEveryLocaleDefinesTheSameKeys`             | klucz obecny w `en`, brakujący gdzie indziej — i odwrotnie |
| `TestPermissionKeysHaveNoOrphanTranslations`    | tłumaczenie po zmienionej nazwie uprawnienia               |
| `TestPolishIsActuallyTranslated`                | katalog skopiowany z angielskiego i niewypełniony          |

Ostatni jest nieoczywisty i konieczny: katalog będący kopią `en` przechodzi wszystkie testy kompletności.

Dodanie języka: [instrukcja tłumaczeń](../guides/008_add_translation.md).
