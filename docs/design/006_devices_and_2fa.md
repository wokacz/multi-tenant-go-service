# Urządzenia i drugi składnik

Każde logowanie jest przypisane do urządzenia. To ono decyduje, czy trzeba podać drugi składnik, i to je odwołuje się,
żeby wyprosić kogoś z konta.

## Token urządzenia

Klient trzyma nieprzezroczysty sekret i odsyła go w nagłówku:

```
X-Device-Token: <32 losowe bajty, base64url>
```

Baza przechowuje **wyłącznie SHA-256** tego sekretu, w `devices.fingerprint`
(64 znaki hex — stąd szerokość kolumny). Zrzut tabeli nie pozwala więc podszyć się pod cudze zaufane urządzenie.

Token jest wydawany **raz**, w odpowiedzi na pierwsze logowanie z danego klienta, w polu `device_token`. Nie da się go
później odzyskać. Klient, który go zgubi, po prostu wygląda jak nowe urządzenie.

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

Wyszukiwanie jest zawężone do konta (`UNIQUE (user_id, fingerprint)`). Dwa konta na tej samej przeglądarce legalnie
dzielą odcisk, ale token jednego nigdy nie rozwiąże się na urządzenie drugiego.

Nierozpoznany token **nie jest błędem** — tak samo wygląda token z innego konta i token z bazy, którą w międzyczasie
wyczyszczono.

## Zaufanie

Urządzenie staje się zaufane, gdy przejdzie weryfikację kodu 2FA **albo** jest tym, z którego 2FA włączono.

Ten drugi przypadek nie jest wygodą, tylko zabezpieczeniem: konto, którego adres e-mail przestał odbierać pocztę,
zablokowałoby się własnym ustawieniem, bez drogi powrotnej innej niż reset hasła na ten sam martwy adres.

Zaufanie liczy się tylko przy włączonym 2FA. Przy wyłączonym nic nie zmienia.

## Odwoływanie

```
DELETE /v1/me/devices/{id}
```

Odwołanie **kasuje zaufanie** i blokuje logowanie z tego urządzenia. Token już wydany przestaje działać przy najbliższym
żądaniu, bo middleware sprawdza urządzenie za każdym razem.

- Odwołanie własnego urządzenia jest dozwolone — to jest „wyloguj tutaj".
- Odwołanie dwukrotne kończy się sukcesem; klient nie musi pisać obsługi konfliktu.
- Cudze `id` to `404`, nie `403` — inaczej odpowiedź potwierdzałaby istnienie rekordu.

Reguły („odwołanie kasuje zaufanie", „odwołanego nie da się zaufać") mieszkają w
`models.Device`. Repozytorium czyta wiersz `SELECT … FOR UPDATE` i je stosuje, zamiast zapisywać drugą kopię tych reguł
w SQL-u.

## Historia logowań

```
GET /v1/me/login-events?limit=50
```

Zapisywane wyniki: `success`, `bad_password`, `mfa_failed`, `locked` (próba z odwołanego urządzenia lub zawieszonego
konta). Każdy wpis niesie adres IP, user agent i urządzenie, o ile udało się je ustalić.

**Próby na nieistniejący adres nie są zapisywane.** `login_events.user_id` jest
`NOT NULL` i nie ma konta, do którego można by je przypisać; tabela kluczowana adresem z żądania byłaby składem
nieuwierzytelnionych danych, którego nic w tym serwisie by nie sprzątało. Praktyczny skutek: pustej historii nie da się
zapchać z zewnątrz.

Adres IP to **realny peer TCP**, nigdy nagłówek. `X-Forwarded-For` może ustawić każdy. Za proxy trzeba podstawić
middleware ufające wyłącznie znanym adresom proxy — `RealIP` z chi tego nie robi.

| Dane                        | Zachowanie                                                                                     |
|-----------------------------|------------------------------------------------------------------------------------------------|
| User agent > 512 znaków     | przycinany, nie odrzucany — dziwny klient nie powinien móc zepsuć logowania                    |
| Adres IP nie do sparsowania | zapisywany jako `0.0.0.0`; kolumna `inet` jest `NOT NULL`, a utrata całego wpisu byłaby gorsza |

---

## Wspólna mechanika kodów

Drugi składnik i reset hasła to dwa przepływy o jednej mechanice: sześć cyfr wysłanych mailem, przechowywanych jako
HMAC, z limitem czasu i limitem prób.

| Właściwość | Reset hasła | Logowanie 2FA          |
|------------|-------------|------------------------|
| Długość    | 6 cyfr      | 6 cyfr                 |
| Ważność    | 15 minut    | 10 minut               |
| Limit prób | 5           | 5                      |
| Powiązanie | konto       | konto **i urządzenie** |

Kod logowania żyje krócej, bo jest oczekiwany w tej samej minucie. Kod resetu może wymagać otwarcia skrzynki.

### Przechowywany jest HMAC, nie kod

```
HMAC-SHA256( AUTH_RESET_SECRET, przeznaczenie ‖ id ‖ kod )
```

`AUTH_RESET_SECRET` (min. 32 bajty) jest **osobnym sekretem** od
`AUTH_TOKEN_SECRET`. Rotacja sekretu podpisującego tokeny nie może unieważniać kodów, które leżą już w czyjejś skrzynce.

Prefiks przeznaczenia (`password-reset` / `two-factor`) rozdziela oba światy. Bez niego ten sam kod hashowałby się do
tej samej wartości w obu tabelach i kod resetu dałoby się wydać jako kod logowania.

### Licznik prób rusza się w SQL-u

Nieudana próba to **jeden warunkowy `UPDATE`**, który inkrementuje licznik i — przy osiągnięciu limitu — od razu spala
kod:

```sql
UPDATE …
   SET attempts    = attempts + 1,
       consumed_at = CASE WHEN attempts + 1 >= $limit THEN $now ELSE consumed_at END
 WHERE id = $id AND consumed_at IS NULL
```

Wariant „wczytaj wiersz, `attempts++`, zapisz" wygląda naturalnie i jest zły:
nakładające się próby odczytują tę samą wartość i zapisują tę samą wartość, więc pięć równoległych strzałów zostawia
licznik na jedynce. Spóźniony zapis mógłby nawet przywrócić `consumed_at`, które inne żądanie właśnie ustawiło, i ożywić
spalony kod.

Pilnują tego `TestFailPasswordResetUnderConcurrency` i
`TestFailTwoFactorChallengeIsAtomic` — na prawdziwym Postgresie.

---

## Logowanie dwuskładnikowe

Włącza się per konto:

```
PUT /v1/me/two-factor      { "password": "…", "enabled": true }
```

Wymaga **aktualnego hasła** obok tokenu. Skradziony token nie może wyłączyć mechanizmu, który istnieje właśnie po to,
żeby ograniczyć skutki kradzieży tokenu — ani włączyć go i zamknąć właściciela na zewnątrz.

### Przepływ

1. `POST /v1/sessions` — hasło poprawne, ale urządzenie niezaufane.
2. Odpowiedź `202` z `two_factor_required: true`. **Bez tokenu.**
3. Kod idzie mailem. W odpowiedzi go nie ma.
4. `POST /v1/sessions/verify` z kodem i tym samym `X-Device-Token`.
5. Odpowiedź `201` z tokenem. Urządzenie zostaje zaufane.

Urządzenie zaufane pomija kroki 2–4 przy kolejnych logowaniach.

### Kod jest przypisany do urządzenia

Wyzwanie zapisuje `device_id`, a `verify` wymaga tego samego tokenu urządzenia. Bez tego powiązania kod odczytany ze
skrzynki dowodziłby tylko dostępu do skrzynki — a nie tego, że maszyna prosząca o wejście jest tą, dla której kod
powstał.

### Wszystko idzie tym samym błędem

`POST /v1/sessions/verify` jest osiągalne **bez poświadczeń**, więc nieznany adres, nieznane urządzenie, brak wyzwania,
cudze urządzenie, zły kod, kod wygasły i kod spalony zwracają jeden `401`. Wyjątkami są odwołane urządzenie i zawieszone
konto (`403`) — właściciel ma prawo wiedzieć, że sam je zablokował albo że zrobił to administrator.

### Błąd dostarczenia nie jest ukrywany

Jeśli wysyłka maila zawiedzie, odpowiedź to `5xx`, nie `202`. Dzwoniący już udowodnił znajomość hasła, więc błąd nie
mówi mu nic nowego o koncie, a `202` za kod, który nigdy nie wyszedł, zostawiłoby go czekającym w nieskończoność.

Reset hasła zachowuje się **odwrotnie** i z dobrego powodu — patrz
[Uwierzytelnianie](005_authentication.md#reset-hasła).
