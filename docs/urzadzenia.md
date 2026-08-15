# Urządzenia

Każde logowanie jest przypisane do urządzenia. To ono decyduje, czy trzeba
podać drugi składnik, i to je odwołuje się, żeby wyprosić kogoś z konta.

## Token urządzenia

Klient trzyma nieprzezroczysty sekret i odsyła go w nagłówku:

```
X-Device-Token: <32 losowe bajty, base64url>
```

Baza przechowuje **wyłącznie SHA-256** tego sekretu, w
`devices.fingerprint` (64 znaki hex — stąd szerokość kolumny). Zrzut tabeli nie
pozwala więc podszyć się pod cudze zaufane urządzenie.

Token jest wydawany **raz**, w odpowiedzi na pierwsze logowanie z danego
klienta, w polu `device_token`. Nie da się go później odzyskać. Klient, który
go zgubi, po prostu wygląda jak nowe urządzenie.

## Rozpoznawanie

```
token przyszedł?  ──nie──▶  wygeneruj nowy, załóż rekord
       │ tak
       ▼
pasuje do urządzenia TEGO konta?  ──nie──▶  wygeneruj nowy, załóż rekord
       │ tak
       ▼
użyj istniejącego
```

Wyszukiwanie jest zawężone do konta (`UNIQUE (user_id, fingerprint)`). Dwa
konta na tej samej przeglądarce legalnie dzielą odcisk, ale token jednego nigdy
nie rozwiąże się na urządzenie drugiego.

Nierozpoznany token **nie jest błędem** — tak samo wygląda token z innego konta
i token z bazy, którą w międzyczasie wyczyszczono.

## Zaufanie

Urządzenie staje się zaufane, gdy:

- przejdzie weryfikację kodu 2FA, albo
- jest tym, z którego włączono 2FA.

Ten drugi przypadek nie jest wygodą, tylko zabezpieczeniem: konto, którego
adres e-mail przestał odbierać pocztę, zablokowałoby się własnym ustawieniem,
bez drogi powrotnej innej niż reset hasła na ten sam martwy adres.

Zaufanie liczy się tylko przy włączonym 2FA. Przy wyłączonym nic nie zmienia.

## Odwoływanie

```
DELETE /v1/me/devices/{id}
```

Odwołanie **kasuje zaufanie** i blokuje logowanie z tego urządzenia. Token już
wydany przestaje działać przy najbliższym żądaniu, bo middleware sprawdza
urządzenie za każdym razem.

- Odwołanie własnego urządzenia jest dozwolone — to jest „wyloguj tutaj".
- Odwołanie dwukrotne kończy się sukcesem. Klient nie musi pisać obsługi konfliktu.
- Cudze `id` to `404`, nie `403` — inaczej odpowiedź potwierdzałaby istnienie rekordu.

Reguły („odwołanie kasuje zaufanie", „odwołanego nie da się zaufać") mieszkają
w `models.Device`. Repozytorium czyta wiersz `SELECT … FOR UPDATE` i stosuje je,
zamiast zapisywać drugą kopię tych reguł w SQL-u.

## Historia logowań

```
GET /v1/me/login-events?limit=50
```

Zapisywane są wyniki: `success`, `bad_password`, `mfa_failed`, `locked`
(próba z odwołanego urządzenia). Każdy wpis niesie adres IP, user agent i
urządzenie, o ile udało się je ustalić.

**Próby na nieistniejący adres nie są zapisywane.** `login_events.user_id` jest
`NOT NULL` i nie ma konta, do którego można by je przypisać; tabela kluczowana
adresem z żądania byłaby składem nieuwierzytelnionych danych, którego nic w tym
serwisie by nie sprzątało. Praktyczny skutek: pustej historii nie da się zapchać
z zewnątrz.

Adres IP to **realny peer TCP**, nigdy nagłówek. `X-Forwarded-For` może ustawić
każdy. Za proxy trzeba podstawić middleware ufające wyłącznie znanym adresom
proxy — `RealIP` z chi tego nie robi.

## Ograniczenia kolumn

| Dane | Zachowanie |
| --- | --- |
| User agent > 512 znaków | Przycinany, nie odrzucany — dziwny klient nie powinien móc zepsuć logowania |
| Adres IP nie do sparsowania | Zapisywany jako `0.0.0.0`; kolumna `inet` jest `NOT NULL`, a utrata całego wpisu audytu byłaby gorsza |
