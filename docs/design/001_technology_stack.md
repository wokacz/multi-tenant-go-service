# Stack technologiczny

Serwis HTTP w Go z Postgresem. Kontrakt API jest generowany z kodu i commitowany. Opcjonalny klient Angular w
`clients/web/` nie jest częścią obrazu API.

## Zależności bezpośrednie

| Biblioteka                           | Wersja  | Do czego                                         |
|--------------------------------------|---------|--------------------------------------------------|
| [huma](https://huma.rocks/)          | v2.39.1 | operacje, walidacja wejścia, generowanie OpenAPI |
| [chi](https://github.com/go-chi/chi) | v5.3.1  | router i middleware                              |
| [ent](https://entgo.io/)             | v0.14.6 | schemat, klient, migracje; dostęp do bazy        |
| [pgx](https://github.com/jackc/pgx)  | v5.10.0 | sterownik Postgresa pod `database/sql`           |
| `github.com/google/uuid`             | v1.6.0  | identyfikatory UUIDv7                            |
| `golang.org/x/crypto`                | v0.55.0 | bcrypt                                           |
| `golang.org/x/text`                  | v0.41.0 | negocjacja języka (`Accept-Language`)            |
| OpenTelemetry (`go.opentelemetry.io/*`) | v1.45.0 | ślady, metryki, logi — opcjonalny eksport OTLP; patrz [010](../design/010_observability.md) |

Go **1.26.6**, wersja w `go.mod`. CI czyta ją stamtąd (`go-version-file`). Obraz developerski pinuje minor tagiem
`golang:1.26-alpine` — `FROM` nie czyta `go.mod`.

## Narzędzia

| Narzędzie                                          | Do czego                                                                        |
|----------------------------------------------------|---------------------------------------------------------------------------------|
| [Task](https://taskfile.dev/)                      | wszystkie polecenia projektu; `task check` to dokładnie to, co robi CI          |
| [Atlas](https://atlasgo.io/)                       | migracje wersjonowane; różnicę liczy ent (`tools/migrate`), Atlas renderuje SQL |
| [golangci-lint](https://golangci-lint.run/) **v2** | lint                                                                            |
| Docker Compose                                     | środowisko developerskie: Postgres 18, migracje, API                            |

> **Uwaga:** `.golangci.yml` używa schematu konfiguracji v2. Instalacja ze
> ścieżki bez `/v2/` po cichu wciąga v1, który tej konfiguracji nie przeczyta
> i zgłosi mylący błąd. Szczegóły w [instrukcji środowiska](../guides/001_development_environment.md).

## Czego świadomie nie ma

Lista jest krótka i to jest celowe — każda pozycja to decyzja, nie zaniedbanie.

| Brak                      | Dlaczego                                                                                                           |
|---------------------------|--------------------------------------------------------------------------------------------------------------------|
| biblioteki JWT            | parser HMAC-SHA256 w `internal/auth/token.go`, bez zewnętrznej biblioteki; atak `alg=none` zamknięty jawnie i pokryty testem |
| frameworka testowego      | wystarcza `testing` ze standardowej biblioteki; brak testify to brak drugiego języka asercji                       |
| biblioteki konfiguracji   | `internal/config` czyta zmienne środowiskowe i zwraca **wszystkie** błędy naraz                                    |
| biblioteki rate limitingu | token bucket per adres IP mieści się w `internal/api/limit.go`                                                     |
| biblioteki logowania      | `log/slog` ze standardowej biblioteki                                                                              |
| biblioteki i18n           | katalogi JSON wkompilowane przez `go:embed`, dopasowanie przez `x/text/language`                                   |
| ORM-owego `AutoMigrate`   | zgaduje zmiany kolumn i nigdy nic nie usuwa, więc schemat cicho by się rozjeżdżał                                  |

`go.sum` pozostaje relatywnie krótki — pilnuj go przy dokładaniu zależności.

## Dwa moduły Go i narzędzia

Repozytorium zawiera **dwa** moduły Go:

- główny — serwis (`cmd/api`, …) oraz generatory `tools/openapi` i `tools/migrate`,
- `tools/entgen/` — generator kodu ent, w osobnym module.

`tools/entgen/` jest osobny, bo generator ent wciąga cobra, pflag i bibliotekę do szerokości znaków — a przy okazji
`go mod tidy` w głównym module wybierał wersję jednej z tych zależności, która się nie kompiluje. Nic z tego nie ma
prawa być w grafie zależności serwisu, który rozmawia wyłącznie z Postgresem.

`tools/openapi` i `tools/migrate` mieszkają w głównym module, bo muszą importować `internal/api` i `internal/store/ent`
— to nie są binaria produkcyjne, tylko generatory wołane przez `task openapi` i `task migrate:diff`.

Katalog `cmd/` zostaje dla procesów uruchamianych w deploymencie lub na hoście developerskim: `api`, `bootstrap`,
`seed`.

Praktyczny skutek: `go mod tidy` bez `-C tools/entgen` pomija drugi moduł. `task tidy`
robi oba i CI to sprawdza.

Drugi skutek: obraz developerski **nie zawiera** generatora ent. Generowanie migracji (`task migrate:diff`) wymaga
Atlasa, modułu `tools/entgen` i `tools/migrate` na hoście; stosowanie gotowych plików (`migrate apply`) wystarcza sam
binarny Atlas, i to robi usługa `migrate` w Compose.

## Docker Compose

Compose jest środowiskiem developerskim, nie obrazem produkcyjnym. Nie ma stage'a produkcyjnego w Dockerfile: target,
którego nikt nie wdraża, rozjeżdża się pierwszy.

`task up` podnosi Postgresa 18, stosuje migracje i startuje API z hot-reloadem (`air`). Debugger (`delve`)
jest za profilem `debug`, żeby nie bił się o port 8000.

Usługi żyją w `.docker/compose.yml`. W korzeniu zostaje czteroliniowy `include`, bo Compose odkrywa `compose.yml` tylko
w katalogu roboczym — bez tego każde polecenie wymagałoby `-f`. `.dockerignore` zostaje w korzeniu, bo Docker szuka go w
korzeniu **kontekstu** buildu, a kontekst to całe repozytorium (`go.mod`).

Dwie drogi uruchomienia — kontenery i `task run` na hoście — dzielą `.env`. Compose nadpisuje `API_HOST`,
`API_PORT`, `POSTGRES_HOST`, `POSTGRES_PORT` oraz sekrety JWT (kontener słucha na `0.0.0.0`, więc wbudowane wartości
deweloperskie są odrzucane); reszta znaczy to samo w obu miejscach.

Jak uruchomić: [instrukcja środowiska](../guides/001_development_environment.md).
