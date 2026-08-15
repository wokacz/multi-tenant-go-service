# Tokeny i sesje

## Token

Kompaktowy JWT podpisany HMAC-SHA256. Bez biblioteki — parser ma ~50 linii i
mieści się w `internal/auth/token.go`.

| Claim | Znaczenie |
| --- | --- |
| `sub` | Identyfikator użytkownika |
| `did` | Identyfikator **urządzenia**, do którego token wydano |
| `ver` | **Epoka sesji** w chwili wydania |
| `exp` / `iat` | Wygaśnięcie i moment wydania |

Konfiguracja: `AUTH_TOKEN_SECRET` (min. 32 bajty) i `AUTH_TOKEN_TTL`
(domyślnie `1h`, format czasu Go — gołe `30` jest odrzucane, nie zgadywane).

Produkcja nie wystartuje z sekretem deweloperskim ani bez własnego.

## Kolejność weryfikacji ma znaczenie

`Parse` **najpierw sprawdza podpis**, dopiero potem czyta nagłówek JOSE. To jest
to, co zamyka atak `alg=none`: token z podmienionym algorytmem nie przechodzi
podpisu, więc jego treść nigdy nie jest interpretowana.

Podpis liczony jest z dokładnie tych bajtów, które przyszły w tokenie — nie z
ponownie zserializowanej postaci. Inaczej różnica w kolejności pól albo w
białych znakach dawałaby dwa różne teksty pod jednym podpisem.

Każda ścieżka błędu zwraca ten sam `ErrInvalidToken`. Rozróżnianie
„uszkodzony" od „wygasły" od „zły podpis" pozwoliłoby badać weryfikator
własność po własności.

## Epoka sesji

`users.session_epoch` to licznik kopiowany do tokenu przy wydaniu. Reset hasła
zwiększa go w tej samej transakcji, w której zapisuje nowy hash.

Token wydany przed zmianą ma starą epokę, więc przy kolejnym żądaniu odpada —
mimo poprawnego podpisu i ważnego `exp`. Daje to unieważnianie **bez listy
odwołań**, której trzeba by pilnować i czyścić.

## Unieważnianie — co czego dotyczy

| Zdarzenie | Skutek |
| --- | --- |
| Reset hasła | Padają **wszystkie** sesje konta (epoka +1) |
| Odwołanie urządzenia | Padają sesje **tego urządzenia** (`did` przestaje wskazywać żywy rekord) |
| Wygaśnięcie | Pada pojedynczy token po `AUTH_TOKEN_TTL` |

Nie ma osobnego „wylogowania". `DELETE /v1/me/devices/{id}` na własnym
urządzeniu robi dokładnie to i działa natychmiast.

## Autoryzacja domyślnie odmawia

`requireBearer` uwierzytelnia **każdą** operację, której identyfikatora nie ma
na jawnej liście `publicOperations` w `internal/api/middleware.go`.

Kuszący wariant — czytać blok `Security` operacji i przepuszczać te bez niego —
zawodzi w złą stronę: trasa dodana bez `Security` byłaby po cichu publiczna i
żaden test, który o tym nie wie, by tego nie wyłapał. Tutaj pomyłka idzie w
drugą stronę: zapomniana operacja staje się nieosiągalna, co widać od razu.

Operacje publiczne:

```
health
create-user
create-session
verify-session
request-password-reset
confirm-password-reset
```

Test `TestEveryOperationIsClassified` sprawdza, że lista i bloki `Security` w
specyfikacji zgadzają się **w obie strony** — żeby generowane klienty nie
kłamały.

Odpowiedź `401` niesie `WWW-Authenticate: Bearer realm="…"`, jak wymaga
RFC 7235.
