# Architektura

## Warstwy i kierunek zależności

Zależności idą **do środka**. Warstwa zewnętrzna wie o wewnętrznej, nigdy odwrotnie.

| Warstwa      | Pakiet              | Odpowiedzialność                                               |
|--------------|---------------------|----------------------------------------------------------------|
| Transport    | `internal/api`      | nagłówki, statusy HTTP, uwierzytelnienie, autoryzacja, OpenAPI |
| Kryptografia | `internal/auth`     | podpisuje i weryfikuje JWT; nie wie o HTTP ani o bazie         |
| Reguły       | `internal/domain/*` | co wolno, komu i kiedy; nie wie o HTTP ani o SQL               |
| Trwałość     | `internal/store`    | GORM; tłumaczy błędy sterownika na błędy domenowe              |
| Języki       | `internal/i18n`     | negocjacja `Accept-Language`, katalogi komunikatów             |

```
internal/api ──▶ internal/domain ──▶ internal/store/models
                        ▲
internal/store/repositories ─┘   (implementuje interfejsy domeny)
```

## Dwie granice pilnowane testem, nie recenzją

`internal/architecture_test.go` parsuje importy każdego pliku `.go` pod
`internal/` i przewraca build, gdy:

- cokolwiek poza `internal/api` importuje **huma**,
- cokolwiek poza `internal/store` importuje **gorm**.

To nie są reguły stylu. Framework, który wycieknie do domeny, zamienia „możemy wymienić huma" w przepisanie projektu, a
import łamiący tę zasadę zawsze wygląda niewinnie w pojedynkę — dlatego jest to test, a nie konwencja.

Importy czytane są z drzewa składniowego, nie grepem, więc wzmianka w komentarzu albo w stałej tekstowej nie jest
fałszywym alarmem.

## Konsument posiada interfejs

Interfejs repozytorium deklaruje **pakiet, który go używa**, a nie ten, który go implementuje.

```
internal/domain/user/repository.go        ← deklaruje Repository
internal/store/repositories/user.go       ← implementuje, z asercją
                                            var _ user.Repository = (*User)(nil)
```

Dzięki temu store zależy od domeny, nigdy odwrotnie, a interfejs wymienia wyłącznie to, czego domena naprawdę używa —
nie wszystko, co store potrafi.

Nowy moduł powtarza ten sam układ: `internal/domain/<rzecz>/repository.go` plus
`internal/store/repositories/<rzecz>.go`. Krok po kroku:
[instrukcja repozytoriów](../guides/004_repositories.md).

## Podział `internal/`

```
internal/
├── api/            transport          ← tylko tu żyje huma
│   ├── problem/    błędy domenowe → problem+json
│   ├── reqctx/     adres i user agent na kontekście
│   └── v1/         operacje wersji 1
├── auth/           JWT
├── config/         konfiguracja procesu
├── i18n/           języki
├── mail/           wysyłka
├── domain/         reguły biznesowe
│   ├── audit/      ślad zmian uprawnień
│   ├── authz/      katalog uprawnień i decyzja autoryzacyjna
│   ├── orgs/       organizacje, członkostwa, role
│   └── user/       konta, hasła, urządzenia, drugi składnik
└── store/          trwałość            ← tylko tu żyje GORM
    ├── models/     modele GORM (źródło prawdy dla schematu)
    └── repositories/
        └── memory/ fake in-memory, wspólny dla wszystkich testów
```

Infrastruktura (`api`, `auth`, `config`, `store`, `i18n`, `mail`) leży bezpośrednio pod `internal/`. Byty biznesowe
mieszkają pod `internal/domain/`, żeby przyszła domena o nazwie `config` nie zderzyła się z konfiguracją procesu, a
`auth` (kryptografia tokenu) nie trafił do drzewa domenowego.

## Błąd zmienia słownictwo dokładnie dwa razy

```
sterownik  ──▶  błąd domenowy  ──▶  status HTTP
           repo             problem
```

1. **Repozytorium** zamienia błąd GORM-a na domenowy (`gorm.ErrDuplicatedKey` → `user.ErrEmailTaken`). Tu kończy się
   GORM.
2. **`internal/api/problem`** zamienia domenowy na status HTTP i kod. Nie wie, że istnieje baza danych.

Cokolwiek niezmapowanego staje się nieprzejrzystym `500`, a prawdziwy błąd trafia do logu przy identyfikatorze żądania.
Surowe błędy niosą nazwy tabel i fragmenty zapytań — to nie należy do odpowiedzi.

`problem` jest osobnym pakietem, a nie plikiem `internal/api/errors.go`, bo
`api` importuje `v1`, żeby zarejestrować trasy — więc `v1` nie może importować
`api`. Pakiet nazwany `errors` przesłaniałby bibliotekę standardową.

Pełna mapa błędów: [Błędy i języki](008_errors_and_i18n.md).

## `main.go` jest nudny

`cmd/api/main.go` składa zależności i nic więcej. Nawet konstrukcja loggera mieszka w `internal/config`, żeby drugie
wejście do aplikacji — worker, CLI — składało te same elementy, nie dziedzicząc decyzji podjętych w `main`.

Jedyny wyjątek to `time.Local = time.UTC`: pgx dekoduje `timestamptz` do strefy lokalnej, więc bez tego ten sam wiersz
serializowałby się inaczej na laptopie i na serwerze.

## Polecenia

Wszystko, co sprawdza CI, idzie przez Task. Uruchomienie serwisu ma dwie drogi:
`docker compose up` albo `task run` na hoście — [instrukcja środowiska](../guides/001_development_environment.md).

```bash
docker compose up   # Postgres, migracje, API z hot-reloadem
task check          # tidy + lint + test + openapi:check
task run            # serwis na hoście (wymaga Postgresa i migracji)
task test           # go test ./... -race
task migrate        # zastosuj migracje
```
