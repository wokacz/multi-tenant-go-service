# Ochrona

Mechanizmy, które nie są autoryzacją, ale bez których autoryzacja niewiele
znaczy.

## Odporność na enumerację kont

Żadna odpowiedź nie powinna zdradzać, czy dany adres jest zarejestrowany.

| Endpoint | Zabieg |
| --- | --- |
| `POST /v1/users` | Duplikat adresu daje `204`, tak samo jak nowa rejestracja |
| `POST /v1/sessions` | Złe hasło i nieznany adres dzielą jeden `401` |
| `POST /v1/password-resets` | Zawsze `204`, także przy awarii zapisu i wysyłki |
| `POST /v1/sessions/verify` | Wszystkie tryby porażki dzielą jeden `401` |
| `GET /v1/users/{id}` | Cudze `id` to `404`, nie `403` |

**Czas też jest kanałem.** Logowanie na nieistniejący adres uruchamia bcrypt na
sztucznym hashu, żeby trwało tyle samo, co logowanie na istniejący. Ta sama
sztuczka jest w przepływach kodów: obliczenie HMAC wykonuje się także wtedy,
gdy nie ma czego porównywać.

Zapis nieudanego logowania do historii jest **celowo ignorowany przy błędach**.
Gdyby awaria zapisu propagowała się jako `500`, złe hasło dawałoby `500`, a
nieznany adres nadal `401` — czyli dokładnie ten orakl, który wspólny błąd miał
zamknąć.

## Limity zapytań

Token bucket per adres IP, oddzielne kubełki dla trzech grup:

| Zmienna | Domyślnie | Chroni |
| --- | --- | --- |
| `REGISTER_PER_MINUTE` | 5 | `POST /v1/users` |
| `LOGIN_PER_MINUTE` | 5 | `POST /v1/sessions` **i** `POST /v1/sessions/verify` |
| `RESET_PER_MINUTE` | 5 | `POST /v1/password-resets` i `…/confirm` |

Oba kroki logowania dzielą kubełek — to jedno logowanie, a osobny kubełek byłby
tylko drugim miejscem do zgadywania.

Klucz to realny peer TCP, nigdy nagłówek, więc podszywanie się pod
`X-Forwarded-For` nie tworzy nowych kubełków. Mapa kubełków jest czyszczona z
wpisów, które zdążyły się w pełni odnowić, więc skan po wielu adresach jej nie
rozdmucha.

Wartość `0` wyłącza limiter — to ustawienie wyłącznie testowe.

Odpowiedź `429` niesie `Retry-After` i jest zwykłym dokumentem `problem+json`.

## Koszt hashowania

bcrypt jest wolny z założenia. Przy koszcie 10 to ~60–85 ms na hash, czyli
proces bez ograniczeń zjada CPU pod byle serią logowań.

Semafor dopuszcza **2 równoległe operacje**. Kluczowe jest to, że oczekiwanie
respektuje kontekst żądania:

```go
if err := ctx.Err(); err != nil { return err }   // ← sprawdzane PRZED select
select {
case s.hashes <- struct{}{}:
case <-ctx.Done():
        return ctx.Err()
}
```

Sprawdzenie przed `select` nie jest nadmiarowe: `select`, którego oba przypadki
są gotowe, wybiera **losowo**, więc anulowany klient i tak startowałby hash
mniej więcej w połowie przypadków — a zachowanie byłoby nieodtwarzalne.

Bez respektowania kontekstu każde żądanie w kolejce zostawałoby zaparkowane
także po rozłączeniu klienta i nadal zajmowałoby slot, gdy w końcu przyszłaby
jego kolej. Kolejka rosłaby szybciej, niż się opróżnia.

Anulowanie jest odróżniane od złego hasła. Inaczej rozłączony klient
dostawałby „złe hasło", a w historii logowań pojawiałby się `bad_password`,
który nigdy się nie zdarzył.

Koszt można zmienić przez `user.WithBcryptCost` — produkcja powinna zostawić
domyślny, testy używają najniższego.

## Nagłówki i granice żądania

Ustawiane dla każdej odpowiedzi:

```
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
Referrer-Policy: no-referrer
Permissions-Policy: camera=(), microphone=(), geolocation=()
Cache-Control: no-store
Cross-Origin-Opener-Policy: same-origin
Cross-Origin-Resource-Policy: same-origin
Strict-Transport-Security: …        (tylko gdy proces sam obsługuje TLS)
```

`MAX_REQUEST_BYTES` (domyślnie 1 MiB) ogranicza ciało żądania.
`ReadHeaderTimeout` to jedyny timeout będący kontrolą bezpieczeństwa, a nie
udogodnieniem — bez niego klient trzyma połączenie w nieskończoność, sącząc
nagłówki po bajcie.

Panika kończy się dokumentem `problem+json` i wpisem w logu przy identyfikatorze
żądania. `Recoverer` z chi nie jest używany: odpowiada zwykłym tekstem i drukuje
stos na stderr, gdzie nic nie wiąże go z żądaniem.

## Konfiguracja produkcyjna

`config.Load()` zwraca **wszystkie** błędy naraz. Konfigurację poprawia się
edycją pliku i restartem, więc jeden błąd na restart zamienia literówkę w powolną
zgadywankę.

Produkcja nie wystartuje, gdy:

- `AUTH_TOKEN_SECRET` lub `AUTH_RESET_SECRET` jest krótszy niż 32 bajty albo ma
  wartość deweloperską,
- nasłuch jest spoza loopbacku bez TLS,
- `POSTGRES_SSL_MODE` to nie `require` / `verify-ca` / `verify-full`,
- hasło do Postgresa jest jednym ze znanych słabych,
- brakuje `SMTP_HOST` — bez niego kody nie mają jak dojść.

W produkcji nie są serwowane `/docs`, `/openapi.json` ani `/schemas`. Kontrakt
to commitowany [`api/openapi.yaml`](../api/openapi.yaml); proces nie publikuje
mapy samego siebie.
