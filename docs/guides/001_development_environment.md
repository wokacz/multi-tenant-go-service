# Środowisko developerskie

Są dwie równorzędne drogi uruchomienia. Obie czytają ten sam `.env`. Compose nadpisuje wyłącznie cztery wartości, które
nie mogą znaczyć tego samego w obu miejscach: `API_HOST`, `API_PORT`, `POSTGRES_HOST`, `POSTGRES_PORT`.

## Czego potrzebujesz

### W kontenerach

| Narzędzie | Po co                                  |
|-----------|----------------------------------------|
| Docker    | Postgres, migracje, API z hot-reloadem |

Do samego `docker compose up` nic więcej nie trzeba. `task check`,
`task migrate:diff` i testy z `-race` wymagają narzędzi hosta — obraz nie zawiera `loader/` ani lintera, a detektor
wyścigów nie działa na Alpine.

### Na hoście

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

### W kontenerach — jedno polecenie

```bash
cp .env.example .env
docker compose up
```

Podnosi Postgresa, czeka aż będzie zdrowy, stosuje migracje, a potem startuje API z hot-reloadem. Pierwszy start
kompiluje cały moduł (healthcheck ma 90 s zapasu). Zapis pliku `.go` albo `.json` w katalogu tłumaczeń przebudowuje i
restartuje serwis w kilka sekund — nic nie trzeba robić ręcznie.

Dlaczego nie ma obrazu produkcyjnego i czemu `loader/` nie jest w obrazie:
[stack](../design/001_technology_stack.md#docker-compose).

### Na hoście

```bash
cp .env.example .env
docker compose up -d postgres
task migrate
task run
```

Szybsze przy intensywnym cyklu edycja–kompilacja i to jedyna droga dla
`task migrate:diff`, który potrzebuje modułu `loader/` (celowo nieobecnego w obrazie —
patrz [stack](../design/001_technology_stack.md#dwa-moduły-go)).

**Nie uruchamiaj obu naraz na tym samym porcie.** Objaw jest mylący: `docker
compose ps` pokazuje kontener jako zdrowy, bo healthcheck sprawdza go od środka, a żądania z hosta trafiają do procesu,
który zajął port pierwszy. Sprawdzenie:

```bash
lsof -nP -iTCP:4000 -sTCP:LISTEN
```

Żeby oddać port 4000 kontenerowi albo odwrotnie:

```bash
docker compose stop api   # Postgres zostaje; host odzyskuje 4000
task run
```

Sprawdzenie, że działa (obie drogi, ten sam adres):

```bash
curl -s http://127.0.0.1:4000/health
open http://127.0.0.1:4000/docs      # Swagger UI, tylko w developmencie
```

### Debugowanie

```bash
docker compose --profile debug up api-debug
```

Uruchamia **drugi** kontener z tym samym kodem pod delve — obok zwykłego, więc nie trzeba niczego zatrzymywać. API
odpowiada na `4001`, debugger słucha na
`2345`; podłącz IDE jako „Go Remote" na `127.0.0.1:2345`. Proces startuje bez czekania na klienta, więc można podpiąć
się i odpiąć w dowolnym momencie.

Kompilacja jest bez optymalizacji i inline'owania, żeby breakpointy trafiały we właściwe linie — dlatego start trwa
dłużej niż zwykłego serwisu.

### Przydatne polecenia kontenerowe

```bash
docker compose logs -f api          # log z hot-reloadem
docker compose exec api sh          # powłoka w kontenerze
docker compose down                 # zatrzymaj (dane i cache zostają)
docker compose down -v              # zatrzymaj i skasuj dane oraz cache
docker compose build --no-cache api # przebuduj obraz po zmianie Dockerfile
```

Cache modułów i buildu Go leżą w nazwanych wolumenach, więc `docker compose
down` nie kasuje ich i kolejny start nie jest zimną kompilacją. Kasuje je dopiero `-v`.

Na Linuksie, gdy UID hosta nie jest 1000, przebuduj obraz z własnym użytkownikiem — inaczej cache w wolumenie będzie
należał do UID 1000:

```bash
docker compose build --build-arg UID=$(id -u) --build-arg GID=$(id -g)
```

Na Docker Desktop (macOS, Windows) mapowanie właściciela robi warstwa wirtualizacji; domyślne 1000 wystarcza.

## Pierwszy administrator

Świeża instalacja nie ma nikogo z uprawnieniami. Zarejestruj konto przez API, a potem nadaj mu rolę właściciela:

```bash
curl -X POST http://127.0.0.1:4000/v1/users \
  -H 'Content-Type: application/json' \
  -d '{"name":"Ada","email":"ada@example.com",
       "password":"twelve-chars","password_confirm":"twelve-chars"}'
```

Na hoście:

```bash
task bootstrap -- -email ada@example.com
```

W kontenerach, bez Go na hoście:

```bash
docker compose exec api go run ./cmd/bootstrap -email ada@example.com
```

Polecenie jest idempotentne. Dlaczego nie „pierwszy zarejestrowany wygrywa":
[Autoryzacja](../design/007_authorization.md#bootstrap).

## Polecenia

| Polecenie                    | Co robi                                                |
|------------------------------|--------------------------------------------------------|
| `docker compose up`          | cały stos w kontenerach                                |
| `task check`                 | **to, co robi CI**: tidy + lint + test + openapi:check |
| `task test`                  | `go test ./... -race`                                  |
| `task lint`                  | golangci-lint                                          |
| `task fmt`                   | formatowanie                                           |
| `task run`                   | serwis na hoście                                       |
| `task build`                 | binarka do `bin/`                                      |
| `task cover`                 | pokrycie + raport HTML                                 |
| `task openapi`               | przegeneruj `api/openapi.yaml`                         |
| `task migrate`               | zastosuj migracje                                      |
| `task migrate:diff NAME=…`   | wygeneruj migrację po zmianie modelu                   |
| `task migrate:status`        | co jest zastosowane                                    |
| `task bootstrap -- -email …` | nadaj pierwszą rolę właściciela                        |
| `task clean`                 | usuń artefakty                                         |

`task --list` pokazuje wszystko z opisami. `task check` i `task migrate:diff`
zawsze idą z hosta.

Nowa migracja wygenerowana na hoście, przy API w kontenerze, nadal wgrywa się przez `task migrate` — Postgres jest
opublikowany na pętli zwrotnej.

## Testy wymagające bazy

Domyślnie `task test` **nie potrzebuje bazy**. Testy repozytoriów pomijają się same, dopóki nie ustawisz zmiennej:

```bash
docker compose up -d postgres && task migrate
POSTGRES_TEST=1 go test ./internal/store/repositories -v
```

Nie wołaj gołego `docker compose up -d`: to podniesie też API i zajmie port 4000.

CI uruchamia je na kontenerze usługowym, więc SQL jest sprawdzany przy każdym pushu.
Szczegóły: [instrukcja testów](007_write_tests.md).

## Konfiguracja

Aplikacja **nie czyta `.env`** — czyta zmienne środowiskowe. Na hoście ładuje je
`dotenv:` w `Taskfile.yml`. W kontenerach wstrzykuje je Compose (`env_file`)
i nadpisuje cztery wartości z początku tej strony. Uruchomienie binarki z pominięciem Taska wymaga własnego
wyeksportowania zmiennych.

Najważniejsze zmienne (pełna lista z komentarzami w `.env.example`):

| Zmienna                             | Domyślnie            | Uwagi                                                  |
|-------------------------------------|----------------------|--------------------------------------------------------|
| `ENV`                               | `development`        | `production` włącza twardą walidację i wyłącza `/docs` |
| `API_HOST` / `API_PORT`             | `127.0.0.1` / `4000` | w kontenerze Compose ustawia host na `0.0.0.0`         |
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
