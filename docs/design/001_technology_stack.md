# Stack technologiczny

Serwis HTTP w Go, z Postgresem, bez frontendu. Kontrakt API jest generowany z kodu i commitowany.

## Zależności bezpośrednie

| Biblioteka                                   | Wersja           | Do czego                                         |
|----------------------------------------------|------------------|--------------------------------------------------|
| [huma](https://huma.rocks/)                  | v2.39.1          | operacje, walidacja wejścia, generowanie OpenAPI |
| [chi](https://github.com/go-chi/chi)         | v5.3.1           | router i middleware                              |
| [GORM](https://gorm.io/) + driver `postgres` | v1.31.2 / v1.6.2 | dostęp do bazy (pgx pod spodem)                  |
| `github.com/google/uuid`                     | v1.6.0           | identyfikatory UUIDv7                            |
| `golang.org/x/crypto`                        | v0.55.0          | bcrypt                                           |
| `golang.org/x/text`                          | v0.41.0          | negocjacja języka (`Accept-Language`)            |

Go **1.26.6**, wersja w `go.mod`. CI czyta ją stamtąd (`go-version-file`). Obraz developerski pinuje minor tagiem
`golang:1.26-alpine` — `FROM` nie czyta `go.mod`.

## Narzędzia

| Narzędzie                                          | Do czego                                                               |
|----------------------------------------------------|------------------------------------------------------------------------|
| [Task](https://taskfile.dev/)                      | wszystkie polecenia projektu; `task check` to dokładnie to, co robi CI |
| [Atlas](https://atlasgo.io/)                       | migracje wersjonowane, generowane z modeli GORM                        |
| [golangci-lint](https://golangci-lint.run/) **v2** | lint                                                                   |
| Docker Compose                                     | środowisko developerskie: Postgres 18, migracje, API                   |

> **Uwaga:** `.golangci.yml` używa schematu konfiguracji v2. Instalacja ze
> ścieżki bez `/v2/` po cichu wciąga v1, który tej konfiguracji nie przeczyta
> i zgłosi mylący błąd. Szczegóły w [instrukcji środowiska](../guides/001_development_environment.md).

## Czego świadomie nie ma

Lista jest krótka i to jest celowe — każda pozycja to decyzja, nie zaniedbanie.

| Brak                      | Dlaczego                                                                                                           |
|---------------------------|--------------------------------------------------------------------------------------------------------------------|
| biblioteki JWT            | parser HMAC-SHA256 to ~50 linii w `internal/auth/token.go`; atak `alg=none` jest zamknięty jawnie i pokryty testem |
| frameworka testowego      | wystarcza `testing` ze standardowej biblioteki; brak testify to brak drugiego języka asercji                       |
| biblioteki konfiguracji   | `internal/config` czyta zmienne środowiskowe i zwraca **wszystkie** błędy naraz                                    |
| biblioteki rate limitingu | token bucket per adres IP mieści się w `internal/api/limit.go`                                                     |
| biblioteki logowania      | `log/slog` ze standardowej biblioteki                                                                              |
| biblioteki i18n           | katalogi JSON wkompilowane przez `go:embed`, dopasowanie przez `x/text/language`                                   |
| ORM-owego `AutoMigrate`   | zgaduje zmiany kolumn i nigdy nic nie usuwa, więc schemat cicho by się rozjeżdżał                                  |

`go.sum` ma około pięćdziesięciu linii. To jest miara, którą warto pilnować przy dokładaniu zależności.

## Dwa moduły Go

Repozytorium zawiera **dwa** moduły:

- główny — serwis,
- `loader/` — drukuje DDL wynikający z modeli GORM, dla Atlasa.

`loader/` jest osobny, bo `atlas-provider-gorm` wciąga każdy sterownik GORM-a plus gRPC i SDK chmurowe. Nic z tego nie
ma prawa być w grafie zależności serwisu, który rozmawia wyłącznie z Postgresem.

Praktyczny skutek: `go mod tidy` bez `-C loader` pomija drugi moduł. `task tidy`
robi oba i CI to sprawdza.

Drugi skutek: obraz developerski **nie zawiera** `loader/`. Generowanie migracji (`task migrate:diff`) wymaga Atlasa i
tego modułu na hoście; stosowanie gotowych plików (`migrate apply`) wystarcza sam binarny Atlas, i to robi usługa
`migrate`
w Compose.

## Docker Compose

Compose jest środowiskiem developerskim, nie obrazem produkcyjnym. Nie ma stage'a produkcyjnego w Dockerfile: target,
którego nikt nie wdraża, rozjeżdża się pierwszy.

`task up` podnosi Postgresa 18, stosuje migracje i startuje API z hot-reloadem (`air`). Debugger (`delve`)
jest za profilem `debug`, żeby nie bił się o port 8000.

Usługi żyją w `.docker/compose.yml`. W korzeniu zostaje czteroliniowy `include`, bo Compose odkrywa `compose.yml` tylko
w katalogu roboczym — bez tego każde polecenie wymagałoby `-f`. `.dockerignore` zostaje w korzeniu, bo Docker szuka go w
korzeniu **kontekstu** buildu, a kontekst to całe repozytorium (`go.mod`).

Dwie drogi uruchomienia — kontenery i `task run` na hoście — dzielą `.env`. Compose nadpisuje wyłącznie `API_HOST`,
`API_PORT`, `POSTGRES_HOST` i
`POSTGRES_PORT`; reszta znaczy to samo w obu miejscach.

Jak uruchomić: [instrukcja środowiska](../guides/001_development_environment.md).
