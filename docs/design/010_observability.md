# Obserwowalność

Trzy sygnały OpenTelemetry — **ślady, metryki i logi** — jednym przełącznikiem i jednym zamknięciem.

```
                 ┌─ ślady ──▶ Tempo
API ─── OTLP/HTTP ├─ metryki ▶ Prometheus     (grafana/otel-lgtm w jednym kontenerze)
                 └─ logi ───▶ Loki
```

## Wyłączone, dopóki nie ma endpointu

Puste `OTEL_EXPORTER_OTLP_ENDPOINT` znaczy, że **nic nie jest eksportowane**, a proces zachowuje się dokładnie tak, jak
przed dodaniem telemetrii. To decyzja, nie wygoda: stos obserwowalności, bez którego API nie wstaje, jest nowym sposobem
położenia produkcji, a laptop i zestaw testów nie powinny potrzebować kolektora.

Instrumenty są wtedy **no-opami o prawdziwych typach** (`telemetry.Disabled()`), więc żadne miejsce w kodzie nie
rozgałęzia się na „czy telemetria istnieje". Nil-owy `*Telemetry` byłby drugim sposobem powiedzenia tego samego i
kończyłby się panic-iem przy pierwszym odrzuconym żądaniu.

**Propagator jest ustawiany zawsze**, nawet z wyłączonym eksportem: żądanie przychodzące z `traceparent` ma zachować swój
`trace_id` w logach tego procesu niezależnie od tego, czy ten proces cokolwiek gdzieś wysyła.

## Uruchomienie lokalnie

```bash
task otel     # kolektor + Grafana na :3000, OTLP na :4318
task up       # API już ma skonfigurowany endpoint na sieci compose
```

Kolektor jest pod **profilem**, więc zwykłe `task up` nie startuje drugiej bazy i trzech silników składowania na
laptopie. API ma endpoint ustawiony niezależnie od tego: eksporter, który nie ma gdzie wysłać, ponawia i loguje
ostrzeżenie — a to lepsza awaria niż API, które nie startuje bez stosu obserwowalności.

## Co jest instrumentowane

| Warstwa | Czym | Dlaczego tak |
|---|---|---|
| HTTP | `otelhttp` (oficjalne contrib) | konwencje semantyczne to ruchomy cel utrzymywany przez ludzi czytających specyfikację; źle dobrane kubełki histogramu dają wykresy, które wyglądają dobrze i kłamią przy porównaniu między usługami |
| SQL | wrapper sterownika ent w [`internal/store/enttrace.go`](../../internal/store/enttrace.go) | pełna kontrola nad tym, co wchodzi do atrybutów spanu — patrz niżej |
| poczta | dekorator `telemetry.MeteredMailer` | handlery **celowo** przełykają błąd wysyłki (wiersz istnieje, kod da się wysłać ponownie), więc bez tego awaria jest tylko linią logu, na którą nikt nie ma alertu |
| domena | ręczne liczniki | patrz „Metryki domenowe" |

### `http.route`, czyli pułapka, którą łatwo przeoczyć

`otelhttp` uruchamia się **przed** dopasowaniem trasy przez chi, więc sam z siebie nazwie span
`GET /v1/orgs/018f.../members` — z identyfikatorem w nazwie. Sto organizacji to sto serii i trasa, której nie da się
zagregować.

Wewnętrzny middleware ([`internal/api/tracing.go`](../../internal/api/tracing.go)) zmienia nazwę spanu po dopasowaniu, i
— to jest ta druga połowa — **dokłada trasę do labelera** `otelhttp`. Ustawienie atrybutu na spanie nie wystarcza:
histogram czasu jest zapisywany po powrocie z handlera, z atrybutów zebranych przez labeler, i bez tego każde żądanie
jest jedną nierozróżnialną serią — czyli wykresem opóźnień, który nie odróżni wolnego raportu od szybkiego health-checku.

### SQL w spanie: **placeholdery, nigdy wartości**

To warunek, na jakim ta instrumentacja w ogóle może istnieć. Zapytanie w atrybucie niesie `$1`, `$2`, … i nigdy
wartości. **Atrybut
spanu to linia logu z innym okresem retencji i innym zbiorem osób, które mogą ją czytać** — trace niosący wartości cicho
cofałby decyzję, z pakietu, którego nikt nie kojarzy z prywatnością.

Przypina to `TestASpanNeverCarriesQueryValues`: bierze adres i hash z utworzonego konta i sprawdza, że **żaden** atrybut
**żadnego** spanu ich nie zawiera — a przy okazji, że SQL z `$1` tam jest, bo trace bez zapytania nie odpowie, które
zapytanie było wolne.

Nazwa tabeli pochodzi z SQL-a: złączenie zapisane jako `memberships AS m` inaczej twierdziłoby, że najruchliwsza tabela
w instalacji nazywa się „m".

Chybienie **nie jest** błędem na spanie. Zapytanie wraca z zerową liczbą wierszy, repozytorium mapuje to na błąd
domenowy, a API na 404 — malowanie każdego chybienia na czerwono uczy wszystkich ignorować czerwone.

## Metryki domenowe

Generyczne liczby HTTP i SQL przychodzą z instrumentacji. Te opisują **ten** produkt i odpowiadają na pytania, które ktoś
naprawdę zadaje o działającą instalację:

| Metryka | Atrybuty | Pytanie, na które odpowiada |
|---|---|---|
| `auth.sign_ins` | `outcome` | ile logowań pada na złym haśle, ile na 2FA, ile na zawieszeniu |
| `authz.denials` | `permission`, `scope` | które uprawnienie blokuje ludzi — pytanie o konfigurację ról, nie o kod |
| `http.rate_limited` | `route` | czy limiter odrzuca prawdziwy ruch (to on powiedziałby o M12 przed człowiekiem) |
| `orgs.invitations` | `event` | wysłane minus przyjęte to liczba osób, na które ktoś czeka |
| `mail.failures` | `kind` | każda to człowiek, który nie dostaje kodu |
| `db.queries`, `db.query.duration` | `operation`, `table`, `error` | storage z tej strony puli połączeń |

**`auth.sign_ins` istnieje dlatego, że odpowiedź nie może nieść tej informacji.** Złe hasło i nieznany adres dzielą jeden
błąd, żeby nikt nie używał statusu do sprawdzania, kto ma konto — co czyni ten stosunek niewidocznym z zewnątrz, i
metryka jest jedynym miejscem, gdzie zostaje.

Struktura instrumentów, nie mapa nazw: literówka w nazwie metryki to seria, która po cichu nigdy nie powstaje. Tutaj to
błąd kompilacji. Klucze atrybutów i wartości `outcome` są stałymi z tego samego powodu — licznik dzielony po `outcome` w
jednym miejscu i po `result` w drugim to dwie serie, których nikt nie zsumuje.

**Kardynalność jest pilnowana.** `http.rate_limited` nosi **trasę**, nie ścieżkę: identyfikator organizacji w atrybucie
to nowa seria na tenanta, czyli rachunek za metryki jako zaskoczenie. `signInOutcome` mapuje błędy na skończony zbiór
etykiet, a nie na tekst błędu.

## Logi

Ten sam rekord idzie **do terminala i do kolektora** — `logging.Fanout` woła oba handlery, więc jedno wywołanie zapisuje
w dwóch miejscach. Alternatywą było pamiętanie o tym w każdym miejscu wywołania, czego żadna baza kodu nie utrzymuje
długo. Każdy handler dostaje **klon** rekordu: `slog.Record` to wartość ze wspólnym magazynem atrybutów, a podanie tego
samego dwóm handlerom to sposób, w jaki atrybuty giną w drugim.

**Korelacja wymaga kontekstu.** `log.Info(...)` nie zna śladu; `log.InfoContext(ctx, ...)` zna. Dlatego `requestLogger`
i każde logowanie na ścieżce żądania używa wariantów z kontekstem — inaczej logi i ślady to dwa systemy, które ktoś
koreluje po znaczniku czasu, czyli dokładnie ta robota, którą tracing miał zabrać.

Handler konsolowy dokłada skrócony `trace=` na linii, bo osiem pierwszych znaków wystarcza do rozpoznania śladu na
liście, a szesnaście bajtów hex to nie coś, co się czyta — to coś, co się wkleja.

## Logowanie do terminala

Development dostaje własny handler ([`internal/logging/console.go`](../../internal/logging/console.go)):

```
10:45:13.219 INFO  telemetry               enabled=true endpoint=http://127.0.0.1:4318 service=mtgs
10:45:18.315 WARN  slow query              duration_ms=812
10:45:19.400 ERROR invitation mail failed  error="smtp: connection refused"
```

Wiadomość jest **dopełniana do kolumny**, a nie oddzielana separatorem: gdy komunikaty są już znajome, czyta się
atrybuty. Data nie jest wypisywana — osoba patrząca w terminal wie, jaki jest dzień, a kolumna kosztuje jedenaście
znaków w każdej linii. `error` jest czerwony, bo to jedyny atrybut, którego się szuka, nie czyta.

Kolor wyłącza: `LOG_COLOR=never`, `NO_COLOR` (konwencja ze specyfikacją) albo wyjście, które nie jest terminalem — więc
`task run | tee log.txt` daje czytelny plik. Test sprawdza, że wersja kolorowa po usunięciu escape'ów jest **identyczna**
z niekolorową, bo inaczej kolor byłby drugim formatem z własnymi błędami.

Produkcja dostaje JSON. Oba formaty są ustawialne w obie strony: developer sprawdzający, co dostaje kolektor, chce JSON-a
na laptopie.

## Zamknięcie

`Shutdown` ma **własny timeout**, nie kontekst procesu: ten jest już anulowany w chwili zamykania — to on zakończył serwer
— a batch processor z martwym kontekstem gubi dokładnie te spany, które opisują zamknięcie. Każdy provider jest zamykany
nawet gdy wcześniejszy zawiódł, bo niedomknięty bufor to utrata spanów opisujących to, co poszło źle na sekundę przed
końcem.

## Wersja semconv jest przypięta

Import `semconv/v1.43.0` musi zgadzać się z wersją, której używa `resource.Default()` w zainstalowanym SDK — inaczej
`resource.Merge` odmawia z `conflicting Schema URL` i proces nie startuje. Aktualizacja SDK oznacza przesunięcie tego
importu razem z nią.
