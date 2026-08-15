# Drugi składnik i reset hasła

Dwa przepływy, jedna mechanika: sześć cyfr wysłanych mailem, przechowywanych
jako HMAC, z limitem czasu i limitem prób.

## Wspólna mechanika kodów

| Właściwość | Reset hasła | Logowanie 2FA |
| --- | --- | --- |
| Długość | 6 cyfr | 6 cyfr |
| Ważność | 15 minut | 10 minut |
| Limit prób | 5 | 5 |
| Powiązanie | konto | konto **i urządzenie** |

Kod logowania żyje krócej, bo jest oczekiwany w tej samej minucie. Kod resetu
może wymagać otwarcia skrzynki.

### Przechowywany jest HMAC, nie kod

```
HMAC-SHA256( AUTH_RESET_SECRET, przeznaczenie ‖ id ‖ kod )
```

`AUTH_RESET_SECRET` (min. 32 bajty) jest **osobnym sekretem** od
`AUTH_TOKEN_SECRET`. Rotacja sekretu podpisującego tokeny nie może unieważniać
kodów, które leżą już w czyjejś skrzynce.

Prefiks przeznaczenia (`password-reset` / `two-factor`) rozdziela oba światy.
Bez niego ten sam kod hashowałby się do tej samej wartości w obu tabelach i
kod resetu dałoby się wydać jako kod logowania.

### Licznik prób rusza się w SQL-u

Nieudana próba to **jeden warunkowy `UPDATE`**, który inkrementuje licznik i —
przy osiągnięciu limitu — od razu spala kod:

```sql
UPDATE …
   SET attempts    = attempts + 1,
       consumed_at = CASE WHEN attempts + 1 >= $limit THEN $now ELSE consumed_at END
 WHERE id = $id AND consumed_at IS NULL
```

Wariant „wczytaj wiersz, `attempts++`, zapisz" wygląda naturalnie i jest zły:
nakładające się próby odczytują tę samą wartość i zapisują tę samą wartość, więc
pięć równoległych strzałów zostawia licznik na jedynce. Spóźniony zapis mógłby
nawet przywrócić `consumed_at`, które inne żądanie właśnie ustawiło, i ożywić
spalony kod.

Testy `TestFailPasswordResetUnderConcurrency` i `TestFailTwoFactorChallengeIsAtomic`
pilnują tej własności na prawdziwym Postgresie.

---

## Logowanie dwuskładnikowe

Włącza się per konto:

```
PUT /v1/me/two-factor      { "password": "…", "enabled": true }
```

Wymaga **aktualnego hasła** obok tokenu. Skradziony token nie może wyłączyć
mechanizmu, który istnieje właśnie po to, żeby ograniczyć skutki kradzieży
tokenu — ani włączyć go i zamknąć właściciela na zewnątrz.

### Przepływ

1. `POST /v1/sessions` — hasło poprawne, ale urządzenie niezaufane.
2. Odpowiedź `202` z `two_factor_required: true`. **Bez tokenu.**
3. Kod idzie mailem. W odpowiedzi go nie ma.
4. `POST /v1/sessions/verify` z kodem i tym samym `X-Device-Token`.
5. Odpowiedź `201` z tokenem. Urządzenie zostaje zaufane.

Urządzenie zaufane pomija kroki 2–4 przy kolejnych logowaniach.

### Kod jest przypisany do urządzenia

Wyzwanie zapisuje `device_id`, a `verify` wymaga tego samego tokenu urządzenia.
Bez tego powiązania kod odczytany ze skrzynki dowodziłby tylko dostępu do
skrzynki — a nie tego, że maszyna prosząca o wejście jest tą, dla której kod
powstał.

### Wszystko idzie tym samym błędem

`POST /v1/sessions/verify` jest osiągalne **bez poświadczeń**, więc nieznany
adres, nieznane urządzenie, brak wyzwania, cudze urządzenie, zły kod, kod
wygasły i kod spalony zwracają jeden `401`. Jedynym wyjątkiem jest odwołane
urządzenie (`403`) — właściciel konta ma prawo wiedzieć, że sam je zablokował.

### Dostarczenie błędu nie jest ukrywane

Jeśli wysyłka maila zawiedzie, odpowiedź to `5xx`, nie `202`. Dzwoniący już
udowodnił znajomość hasła, więc błąd nie mówi mu nic nowego o koncie, a `202`
za kod, który nigdy nie wyszedł, zostawiłoby go czekającym w nieskończoność.

Reset hasła zachowuje się **odwrotnie** — patrz niżej.

---

## Reset hasła

```
POST /v1/password-resets            { "email": "…" }
POST /v1/password-resets/confirm    { "email", "code", "password", "password_confirm" }
```

### Prośba zawsze kończy się `204`

Nieznany adres, awaria bazy, awaria SMTP — zawsze `204`. Gdyby błąd zapisu
zwracał `500`, robiłby to **tylko dla zarejestrowanych adresów**, co jest
dokładnie tym oraklem, który wspólna odpowiedź ma zamykać. Awarie trafiają do
logu.

W developmencie bez skonfigurowanego SMTP kod ląduje w logu procesu.

### Potwierdzenie unieważnia sesje

`ConsumePasswordReset` w **jednej transakcji**:

1. zapisuje nowy hash hasła,
2. zwiększa `session_epoch`,
3. oznacza kod jako spalony.

Awaria w połowie nie może zostawić spalonego kodu przy starym haśle. Po
operacji wszystkie wcześniejsze tokeny konta przestają być przyjmowane — patrz
[epoka sesji](tokeny-i-sesje.md#epoka-sesji).
