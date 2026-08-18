# Dokumentacja

Kod jest źródłem prawdy. Te strony tłumaczą **dlaczego** rzeczy wyglądają tak, jak wyglądają, i **jak** dołożyć kolejny
element, nie czytając wszystkiego od zera.

| Katalog              | Zawiera                              | Kiedy tu zaglądasz                                  |
|----------------------|--------------------------------------|-----------------------------------------------------|
| [`design/`](design/) | jak system jest zbudowany i dlaczego | gdy chcesz zrozumieć decyzję albo ją zakwestionować |
| [`guides/`](guides/) | jak dodać kolejny element            | gdy piszesz kod                                     |

## Projekt

| Dokument                                                         | O czym                                                                      |
|------------------------------------------------------------------|-----------------------------------------------------------------------------|
| [001 Stack technologiczny](design/001_technology_stack.md)       | biblioteki, narzędzia, czego świadomie nie ma, dwa moduły Go                |
| [002 Architektura](design/002_architecture.md)                   | warstwy, kierunek zależności, granice pilnowane testem, podział `internal/` |
| [003 Kontrakt API](design/003_api_contract.md)                   | wersjonowanie, generowane OpenAPI, DTO, wymagania wobec operacji            |
| [004 Model danych](design/004_data_model.md)                     | modele jako źródło schematu, Atlas, pułapki GORM-a, spis tabel              |
| [005 Uwierzytelnianie](design/005_authentication.md)             | hasła, token JWT, epoka sesji, co unieważnia co, reset hasła                |
| [006 Urządzenia i drugi składnik](design/006_devices_and_2fa.md) | rozpoznawanie klienta, zaufanie, odwoływanie, kody z maila                  |
| [007 Autoryzacja](design/007_authorization.md)                   | role, uprawnienia, organizacje, anty-eskalacja, migawka, audyt              |
| [008 Błędy i języki](design/008_errors_and_i18n.md)              | `problem+json`, kody błędów, mapa błędów domenowych, i18n                   |
| [009 Odporność](design/009_hardening.md)                         | enumeracja kont, limity, koszt bcrypta, nagłówki, produkcja                 |

## Instrukcje

| Dokument                                                              | Kiedy                                          |
|-----------------------------------------------------------------------|------------------------------------------------|
| [001 Środowisko developerskie](guides/001_development_environment.md) | pierwsze uruchomienie, polecenia, konfiguracja |
| [002 Dodanie endpointu](guides/002_add_endpoint.md)                   | nowa operacja HTTP                             |
| [003 Modele i migracje](guides/003_models_and_migrations.md)          | nowa tabela albo zmiana kolumny                |
| [004 Repozytoria](guides/004_repositories.md)                         | nowy dostęp do danych                          |
| [005 Praca z bazą](guides/005_database_access.md)                     | transakcje, współbieżność, pułapki zapytań     |
| [006 Nowe uprawnienie](guides/006_add_permission.md)                  | nowa rzecz do kontrolowania                    |
| [007 Pisanie testów](guides/007_write_tests.md)                       | zawsze                                         |
| [008 Tłumaczenia](guides/008_add_translation.md)                      | nowy komunikat lub nowy język                  |
| [009 Dane rozwojowe](guides/009_seed_data.md)                         | konta i organizacje do klikania i testów       |

## Szybkie odpowiedzi

| Pytanie                                           | Gdzie                                             |
|---------------------------------------------------|---------------------------------------------------|
| Dlaczego huma nie może wyjść poza `internal/api`? | [design/002](design/002_architecture.md)          |
| Dlaczego uprawnień nie ma w tokenie?              | [design/007](design/007_authorization.md)         |
| Kiedy 403, a kiedy 404?                           | [design/007](design/007_authorization.md)         |
| Co znaczy `code` w odpowiedzi błędnej?            | [design/008](design/008_errors_and_i18n.md)       |
| Dlaczego licznik prób rusza się w SQL-u?          | [design/006](design/006_devices_and_2fa.md)       |
| Skąd się bierze pierwszy administrator?           | [design/007](design/007_authorization.md)         |
| Jak działają zaproszenia e-mail?                  | [design/007](design/007_authorization.md#zaproszenia) |
| Dodałem trasę i dostaję 403 — dlaczego?           | [guides/002](guides/002_add_endpoint.md)          |
| Zmieniłem model i CI protestuje                   | [guides/003](guides/003_models_and_migrations.md) |
| Test przechodzi u mnie, a na CI nie               | [guides/007](guides/007_write_tests.md)           |
| Skąd wziąć konta do klikania po aplikacji?        | [guides/009](guides/009_seed_data.md)             |

## Konwencja

**Katalogi i nazwy plików są po angielsku, treść po polsku.**

Ścieżka jest identyfikatorem: bez znaków diakrytycznych — te różnie normalizują się między macOS a Linuksem i wymagają
kodowania w adresach URL — i w tym samym słowniku co kod, więc `007_authorization.md` odpowiada `internal/domain/authz`.
Polski bez ogonków („bezpieczenstwo") nie jest ani polski, ani angielski. Treść, czyli to, co się faktycznie czyta, jest
po polsku.

Format nazwy: `NNN_nazwa_z_podkresleniami.md`.

Numer wyznacza **kolejność czytania**, nie priorytet. Nowy dokument dopisuje się na końcu, chyba że jego miejsce w
kolejności naprawdę ma znaczenie — wtedy przenumerowanie to `git mv` i poprawka w tym indeksie.

Zasady, które utrzymują to w kupie:

- **Jeden fakt w jednym miejscu.** Powtórzenie zastępujemy odnośnikiem.
- **Jeden dokument, jeden zakres.** Jeśli nie da się go streścić jednym zdaniem w powyższej tabeli, powinien być dwoma
  dokumentami.
- **Dlaczego, nie co.** „Co" da się przeczytać w kodzie i tam się nie zdezaktualizuje.
- **Krótko.** Dokument, którego nikt nie doczyta, przed niczym nie chroni.

## Poza `docs/`

| Gdzie                                     | Co                                                               |
|-------------------------------------------|------------------------------------------------------------------|
| [`README.md`](../README.md)               | czym jest projekt, wymagania, uruchomienie                       |
| [`compose.yml`](../compose.yml)           | wskaźnik `include`, żeby `docker compose up` działało z korzenia |
| [`.docker/`](../.docker/)                 | Compose, obraz i hot-reload; bez konfiguracji produkcyjnej       |
| [`api/openapi.yaml`](../api/openapi.yaml) | kontrakt HTTP — generowany i commitowany                         |
| [`CLAUDE.md`](../CLAUDE.md)               | zasady pracy nad kodem dla asystenta AI                          |
| `/docs` na uruchomionym serwisie          | Swagger UI (tylko development)                                   |
