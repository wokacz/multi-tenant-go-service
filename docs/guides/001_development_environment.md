# Środowisko developerskie

## Czego potrzebujesz

| Narzędzie                                          | Po co                        |
|----------------------------------------------------|------------------------------|
| [Go](https://go.dev/) 1.26+                        | kompilator                   |
| [Task](https://taskfile.dev/)                      | wszystkie polecenia projektu |
| [Atlas](https://atlasgo.io/)                       | migracje                     |
| [golangci-lint](https://golangci-lint.run/) **v2** | lint                         |
| Docker                                             | Postgres lokalnie            |

```bash
brew install go
brew install go-task/tap/go-task
brew install ariga/tap/atlas
brew install golangci-lint
```

> **Pułapka.** `.golangci.yml` używa schematu v2. Instalacja przez `go install`
> **musi** iść ze ścieżki `/v2/`:
>
> ```bash
> go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
> ```
>
> Stara ścieżka po cichu instaluje v1, który tej konfiguracji nie przeczyta i
> zgłosi mylący błąd.

## Pierwsze uruchomienie

```bash
cp .env.example .env
docker compose up -d      # Postgres
task migrate              # schemat
task run                  # serwis na http://127.0.0.1:4000
```

Sprawdzenie:

```bash
curl -s http://127.0.0.1:4000/health
open http://127.0.0.1:4000/docs      # Swagger UI, tylko w developmencie
```

## Pierwszy administrator

Świeża instalacja nie ma nikogo z uprawnieniami. Zarejestruj konto przez API, a potem nadaj mu rolę właściciela:

```bash
curl -X POST http://127.0.0.1:4000/v1/users \
  -H 'Content-Type: application/json' \
  -d '{"name":"Ada","email":"ada@example.com",
       "password":"twelve-chars","password_confirm":"twelve-chars"}'

task bootstrap -- -email ada@example.com
```

Polecenie jest idempotentne. Dlaczego nie „pierwszy zarejestrowany wygrywa":
[Autoryzacja](../design/007_authorization.md#bootstrap).

## Polecenia

| Polecenie                    | Co robi                                                |
|------------------------------|--------------------------------------------------------|
| `task check`                 | **to, co robi CI**: tidy + lint + test + openapi:check |
| `task test`                  | `go test ./... -race`                                  |
| `task lint`                  | golangci-lint                                          |
| `task fmt`                   | formatowanie                                           |
| `task run`                   | serwis                                                 |
| `task build`                 | binarka do `bin/`                                      |
| `task cover`                 | pokrycie + raport HTML                                 |
| `task openapi`               | przegeneruj `api/openapi.yaml`                         |
| `task migrate`               | zastosuj migracje                                      |
| `task migrate:diff NAME=…`   | wygeneruj migrację po zmianie modelu                   |
| `task migrate:status`        | co jest zastosowane                                    |
| `task bootstrap -- -email …` | nadaj pierwszą rolę właściciela                        |
| `task clean`                 | usuń artefakty                                         |

`task --list` pokazuje wszystko z opisami.

## Testy wymagające bazy

Domyślnie `task test` **nie potrzebuje bazy**. Testy repozytoriów pomijają się same, dopóki nie ustawisz zmiennej:

```bash
docker compose up -d && task migrate
POSTGRES_TEST=1 go test ./internal/store/repositories -v
```

CI uruchamia je na kontenerze usługowym, więc SQL jest sprawdzany przy każdym pushu.
Szczegóły: [instrukcja testów](007_write_tests.md).

## Konfiguracja

Aplikacja **nie czyta `.env`** — czyta zmienne środowiskowe. To `dotenv:` w
`Taskfile.yml` ładuje plik. Uruchomienie binarki z pominięciem Taska wymaga własnego wyeksportowania zmiennych.

Najważniejsze zmienne (pełna lista z komentarzami w `.env.example`):

| Zmienna                             | Domyślnie            | Uwagi                                                  |
|-------------------------------------|----------------------|--------------------------------------------------------|
| `ENV`                               | `development`        | `production` włącza twardą walidację i wyłącza `/docs` |
| `API_HOST` / `API_PORT`             | `127.0.0.1` / `4000` |                                                        |
| `AUTH_TOKEN_SECRET`                 | wartość dev          | produkcja wymaga własnej, min. 32 bajty                |
| `AUTH_TOKEN_TTL`                    | `1h`                 | format czasu Go; gołe `30` jest odrzucane              |
| `AUTH_RESET_SECRET`                 | wartość dev          | **osobny** sekret od tokenowego                        |
| `POSTGRES_*`                        | localhost / postgres | produkcja wymaga SSL i mocnego hasła                   |
| `REGISTER_/LOGIN_/RESET_PER_MINUTE` | `5`                  | `0` wyłącza limiter (tylko testy)                      |
| `SMTP_HOST`                         | puste                | bez niego kody lądują w logu (tylko development)       |

Timeouty i rozmiary puli połączeń **nie są** konfigurowalne przez środowisko — są stałymi w `internal/config`.

Błędy konfiguracji zgłaszane są **wszystkie naraz**, żeby literówka nie zamieniła się w serię restartów.

## Zanim wyślesz pull request

```bash
task check
```

Gdy zmieniałeś modele albo trasy, `task check` powie ci to wprost:

- zmiana handlera bez `task openapi` → `openapi:check` zgłasza nieaktualny plik,
- zmiana modelu bez migracji → krok Atlasa w CI zgłasza dryf,
- nowa operacja bez klasyfikacji autoryzacyjnej → test przewraca build.
