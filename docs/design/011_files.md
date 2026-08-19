# Pliki

Przesłany przez API plik jest **weryfikowany**, opcjonalnie skanowany, szyfrowany i zapisywany poza bazą. Metadane zostają
w Postgresie, żeby listowanie, autoryzacja i usuwanie nie wymagały odszyfrowania.

```
multipart ──▶ magic bytes ──▶ (ClamAV) ──▶ AES-256-GCM ──▶ {root}/{orgID}/{fileID}
                                    │
                                    └── wiersz w `files` (nazwa, typ, SHA-256, status skanu)
```

To nie jest dysk użytkownika w chmurze. Cały payload jest buforowany w pamięci — limit rozmiaru jest więc jednocześnie
limitem pamięci. Backend to wyłącznie `local`; nazwa `FILES_STORAGE_BACKEND` istnieje, żeby drugi backend dało się
dodać bez przemianowywania reszty.

## Dlaczego nie ufać `Content-Type`

Nagłówek i rozszerzenie kontroluje klient. To pierwsze, co fałszuje złośliwy upload: `innocent.pdf` z bajtami `MZ` to
wykonywalny Windows, nie dokument.

`Detect` czyta magic bytes. ZIP jest otwierany tylko po liście nazw (OOXML: `word/`, `xl/`, `ppt/`) — bez dekompresji,
żeby zip bomb nie inflował tu. `http.DetectContentType` jest ostatnią deską, nigdy nadpisaniem: plik `MZ` zostaje
wykonywalnym nawet jeśli stdlib nazwałby go `application/octet-stream`.

`application/octet-stream` w nagłówku liczy się jako **niezadeklarowany**, nie jako zgoda na wszystko.

Domyślna lista (`FILES_ALLOWED_TYPES` puste) to pdf, png, jpeg, gif, webp, plain, csv, json, zip oraz docx/xlsx/pptx.
Wildcards nie ma — z tego samego powodu, dla którego nie ma ich w CORS i w uprawnieniach. Wykonywalne są blokowane
osobno (`FILES_BLOCK_EXECUTABLES`), także po rozszerzeniu.

Dwie flagi, obie domyślnie włączone:

| Zmienna                         | Co robi                                                                          |
|---------------------------------|----------------------------------------------------------------------------------|
| `FILES_REQUIRE_DECLARED_MATCH`  | zadeklarowany MIME musi zgadzać się z wykrytym (pusty / octet-stream = brak)     |
| `FILES_REQUIRE_EXTENSION_MATCH` | rozszerzenie, jeśli znane, musi zgadzać się z wykrytym typem                     |

CSV wolno pomylić z `text/plain`: oba są tekstem UTF-8 i sniffer nie ma jak ich rozróżnić.

## Skan złośliwego oprogramowania

ClamAV przez TCP (`INSTREAM`), bez biblioteki klienckiej. Protokół to prefiksowane długością kawałki.

| `FILES_SCAN_MODE` | Gdy skaner odpowiada        | Gdy skaner nie wstaje                         |
|-------------------|-----------------------------|-----------------------------------------------|
| `off`             | nie wołany; status `skipped` | —                                             |
| `optional`        | `clean` albo odmowa         | plik **zostaje**; status `unavailable`        |
| `required`        | `clean` albo odmowa         | upload pada `503 file_scan_unavailable`       |

Zainfekowany payload **nigdy nie jest zapisywany**. Nie ma wartości `infected` w enumeracji: taki wiersz byłby
dowodem, którego nie chcemy trzymać, i kusiłby do ponownego pobrania.

Domyślnie skan jest wyłączony, żeby `task up` nie wymagał drugiej usługi. `task compose:clamav` stawia `clamd` pod
profilem — analogia do `task otel`. Adres w Compose jest ustawiony zawsze; tryb `off` go ignoruje.

Obraz to `clamav/clamav-debian:1.4`, nie Alpine `clamav/clamav`. Oficjalny obraz Alpine jest tylko `linux/amd64`;
Debian jest wieloarchitekturowy (`amd64` / `arm64` / `ppc64le`), więc `task compose:clamav` na Apple Silicon ma co
pobrać. Protokół (`INSTREAM` na 3310) jest ten sam.

To nie jest sandbox ani analiza behawioralna. Sygnaturowy silnik na tym, co zmieściło się w pamięci, nic więcej.
YARA, VirusTotal i skan po zapisie świadomie nie wchodzą.

## Szyfrowanie w spoczynku

AES-256-GCM, jeden klucz procesu, koperta `wersja (1) || nonce (12) || ciphertext+tag`. Wersja jest po to, żeby rotacja
mogła wprowadzić drugi algorytm bez czytania każdego bloba.

`encryption_key_id` to pierwsze 8 bajtów SHA-256 klucza, hex. Nie jest sekretem: mówi przyszłej rotacji, *którym*
kluczem ten blob jeszcze otworzyć.

Klucz: 32 surowe bajty albo 64 znaki hex (`FILES_ENCRYPTION_KEY`). Na loopbacku w developmencie, gdy pusty, wchodzi
wartość z repozytorium. Wszystko inne — w tym Compose na `0.0.0.0` — odmawia startu bez unikalnego. Produkcja odrzuca
też samą wartość deweloperską, nawet gdy ktoś ją wstawi.

Klucz **nie** jest derywowany z organizacji ani z identyfikatora pliku. Tenant isolation to ścieżka
`{root}/{orgID}/{fileID}` i fakt, że każde zapytanie o metadane nosi `orgID` jako drugi argument. Osobny klucz per
najemca byłby drugim sekretem do zgubienia przy pierwszym wdrożeniu.

Aplikacja trzyma plaintext tylko w trakcie żądania. Na dysku jest wyłącznie koperta. `ErrCorrupt` (zła koperta, zły
klucz, obcięty blob) staje się nieprzejrzystym `500`: rozróżnienie „nie da się sparsować" / „tag nie zgadza się" nic
wołającemu nie daje i mogłoby coś powiedzieć atakującemu.

## Gdzie leży blob

```
{FILES_STORAGE_PATH}/{orgID}/{fileID}
{FILES_STORAGE_PATH}/account/{fileID}
```

Obie części ścieżki organizacji to UUID, więc spreparowana nazwa oryginalna nie wychodzi poza katalog. Segment
`account` nie jest UUID-em, więc nie zderzy się z katalogiem najemcy. Zapis idzie przez plik `.tmp` i `rename`.
Katalogi `0700`, pliki `0600`.

Metadane są wstawiane **po** zapisie bloba. Gdy INSERT padnie, blob jest usuwany (kompensata). Usuwanie idzie odwrotnie:
najpierw blob (idempotentnie — braku pliku nie ma), potem wiersz i wpis w dzienniku.

Miękkie usunięcie organizacji **nie** rusza plików: `ON DELETE CASCADE` odpala się tylko przy twardym DELETE, a
organizacje są usuwane miękko. To ta sama pułapka co przy rolach i członkostwach — i powód, dla którego `uploaded_by`
jest zwykłą kolumną UUID, nie krawędzią. Kaskada z konta zabrałaby dowód w momencie, w którym konto znika; `SET NULL`
zabrałby jedyny zapis, kto to wgrał. Miękko usunięte konto zostawia `id`, więc join, którego dziennik i tak używa,
nadal znajduje nazwę.

Osierocone ciphertext po twardym usunięciu organizacji nie są zbierane. Nie ma workera sprzątającego. Świadomie: twardy
DELETE tenanta to zdarzenie operacyjne, nie ścieżka API.

## HTTP

| Operacja        | Ścieżka                                      | Uprawnienie     |
|-----------------|----------------------------------------------|-----------------|
| lista           | `GET /v1/orgs/{orgID}/files`                 | `files.read`    |
| metadane        | `GET /v1/orgs/{orgID}/files/{fileID}`        | `files.read`    |
| treść           | `GET /v1/orgs/{orgID}/files/{fileID}/content`| `files.read`    |
| wgranie         | `POST /v1/orgs/{orgID}/files` (multipart `file`) | `files.create` |
| usunięcie       | `DELETE /v1/orgs/{orgID}/files/{fileID}`     | `files.delete`  |

Lista oddaje metadane, nigdy bajtów. Pobieranie jest załącznikiem (`Content-Disposition`); `Content-Type` to wykryty
typ, nie zadeklarowany.

Organizacja pochodzi z `authz.GrantFrom(ctx)`, nigdy z `in.OrgID` — ta sama reguła co wszędzie indziej. Brak
członkostwa to `404`, nie `403`.

Role shipowane: `admin` ma wszystkie trzy, `member` czyta i wgrywa, `viewer` tylko czyta. `owner` i tak dostaje cały
katalog. `admin` nie jest wyprowadzany z katalogu, więc te trzy klucze są **wypisane** — ciche nadanie nowego
uprawnienia każdemu administratorowi przy niepowiązanej funkcji jest dokładnie tym, czego ta rola unika.

Dziennik: `file.uploaded`, `file.deleted`. Skan i pobranie nie zostawiają wpisu.

## Awatar

Pierwszy konsument tej samej tabeli `files` poza dokumentami organizacji. Zdjęcie profilowe należy do **konta**, nie do
najemcy: ta sama osoba może być w wielu organizacjach, a `GET /v1/users/{id}` i tak oddaje wyłącznie własny rekord.
Dlatego `files.organization_id` jest nullable — wiersz awatara nie ma organizacji — a `users.avatar_id` wskazuje ten
wiersz. Ciphertext leży pod `account/{fileID}`.

To jest wzorzec na później: załącznik zadania to też relacja do `files`, nie kopia metadanych na rodzicu. Listowanie
organizacji filtruje po `organization_id`, więc wiersz z NULL nie wycieka do `GET /v1/orgs/{id}/files`.

Obecność awatara to ustawione `users.avatar_id`. `GET /v1/me` oddaje metadane z tego wiersza (`avatar`, `omitempty`);
bajty są na `GET /v1/me/avatar`. `ByID` konta **nie** joinuje `files` — middleware Bearer woła go na każdym żądaniu.

| Operacja   | Ścieżka                 | Autoryzacja                          |
|------------|-------------------------|--------------------------------------|
| metadane   | w `GET /v1/me`          | samoobsługa (token)                  |
| treść      | `GET /v1/me/avatar`     | samoobsługa                          |
| wgranie    | `POST /v1/me/avatar`    | samoobsługa; multipart `file`        |
| usunięcie  | `DELETE /v1/me/avatar`  | samoobsługa                          |

`/v1/me/*` nie może być pod uprawnieniem — ta sama reguła co profil i urządzenia. Drugie wgranie wstawia **nowy** wiersz
i kasuje poprzedni (blob + wiersz), nie nadpisuje ścieżki. Gdy `CreateAccountFile` albo `AttachAvatar` padnie po `Put`,
nowy blob jest usuwany. Dziennik organizacji **nie** zapisuje zmiany awatara: to samoobsługa, nie zmiana w organizacji.

Typy są **na sztywno** png/jpeg/gif/webp. PDF i ZIP to pliki, nie twarz; `FILES_ALLOWED_TYPES` ich nie wpuszcza. Limit
domyślnie 2 MiB (`FILES_AVATAR_MAX_BYTES`) — osobny od 10 MiB plików organizacji, bo zdjęcie profilowe tej wielkości to
już nie zdjęcie. POST dzieli kubełek `FILES_UPLOAD_PER_MINUTE` z uploadem organizacji.

## Konfiguracja

| Zmienna                          | Domyślnie                         | Uwagi                                                                 |
|----------------------------------|-----------------------------------|-----------------------------------------------------------------------|
| `FILES_STORAGE_BACKEND`          | `local`                           | jedyna zaimplementowana wartość                                       |
| `FILES_STORAGE_PATH`             | `var/files` (dev)                 | produkcja wymaga jawnej; Compose: `/tmp/air/files`                    |
| `FILES_ENCRYPTION_KEY`           | wartość dev na loopbacku          | 32 bajty albo 64 znaki hex                                            |
| `FILES_MAX_BYTES`                | 10 MiB                            | sufit 512 MiB; na POST upload limit ciała = to + 256 KiB na multipart |
| `FILES_AVATAR_MAX_BYTES`         | 2 MiB                             | osobny sufit na `POST /v1/me/avatar`                                 |
| `FILES_ALLOWED_TYPES`            | lista produktu                    | przecinek; `*` jest odrzucane przy starcie                            |
| `FILES_SCAN_MODE`                | `off`                             | `off` / `optional` / `required`                                       |
| `FILES_CLAMAV_ADDR`              | puste                             | wymagane przy `required`                                              |
| `FILES_CLAMAV_TIMEOUT`           | `10s`                             |                                                                       |
| `FILES_UPLOAD_PER_MINUTE`        | 20                                | `0` wyłącza (tylko testy); `POST …/files` i `POST /v1/me/avatar`      |
| `FILES_REQUIRE_DECLARED_MATCH`   | `true`                            |                                                                       |
| `FILES_REQUIRE_EXTENSION_MATCH`  | `true`                            |                                                                       |
| `FILES_BLOCK_EXECUTABLES`        | `true`                            |                                                                       |

`MAX_REQUEST_BYTES` (1 MiB) zostaje limitem JSON-a. Upload, który ma prawo być większy, dostaje własny sufit — inaczej
domyślne 10 MiB (pliki) albo 2 MiB (awatar) byłoby nieosiągalne.

## Czego tu nie ma

- **S3 / GCS / Azure Blob.** Świadomie. Lokalny katalog wystarcza, żeby potok (sprawdzanie typu, skan, koperta) miał prawdziwe
  I/O i dał się testować bez konta w chmurze. Drugi backend to zmiana `filestore`, nie domeny.
- **Skan po zapisie, kolejka, sandbox.** Zainfekowany plik nie ma prawa leżeć na dysku nawet chwilę.
- **Deduplikacja po SHA-256.** Dwa wgrania tego samego dokumentu to dwa wiersze. Współdzielenie bloba między
  organizacjami mieszałoby tenantów; wewnątrz organizacji i tak trzeba osobnej autoryzacji.
- **Szyfrowanie per najemca i rotacja klucza.** Koperta ma pole wersji, `key_id` jest zapisany — to przygotowanie, nie
  implementacja.
