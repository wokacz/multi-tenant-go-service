# Autoryzacja

Uwierzytelnianie odpowiada na pytanie „kim jesteś". Ten dokument jest o drugim pytaniu: **„co ci wolno"**.

## Dwa pytania, dwa miejsca

Decyzja rozkłada się na dwie połowy, rozstrzygane w różnych warstwach. To jest sedno projektu, nie szczegół
implementacji.

| Pytanie                                           | Gdzie                                                    | Jak                                                           |
| ------------------------------------------------- | -------------------------------------------------------- | ------------------------------------------------------------- |
| Czy wołający ma uprawnienie X w organizacji O?    | middleware `requirePermission` (`internal/api/authz.go`) | mapa `operationAccess` + `authz.Authorize`                    |
| Czy zasób, na który działa, leży w organizacji O? | repozytorium                                             | `orgID` jest **obowiązkowym drugim parametrem** każdej metody |

Druga połowa jest strukturalna. Nie da się pobrać roli inaczej niż przez
`Role(ctx, orgID, roleID)`, więc pominięcie kontroli zasięgu jest błędem kompilacji, a nie dziurą, którą ktoś musi
wypatrzeć na review.
`TestScopedRepositoryMethodsTakeAnOrganization` pilnuje tego kształtu.

## Trzy rozłączne kategorie operacji

Rozszerzenie wzorca, który kod stosował już przy `publicOperations`.

| Kategoria     | Zbiór                   | Znaczenie                                      |
| ------------- | ----------------------- | ---------------------------------------------- |
| publiczna     | `publicOperations`      | bez tokenu                                     |
| samoobsługowa | `selfServiceOperations` | token wystarcza — tożsamość _jest_ autoryzacją |
| chroniona     | `operationAccess`       | wymaga uprawnienia w zakresie                  |

Operacja spoza wszystkich trzech zbiorów jest **odrzucana z 403** i logowana jako defekt konfiguracji.
`TestEveryOperationHasExactlyOneAuthorizationRule`
przewraca build, gdy operacja nie należy do żadnego zbioru **albo** należy do dwóch — drugie jest gorsze, bo wtedy
pierwszy sprawdzany zbiór po cichu wygrywa, a drugi wpis wygląda jak reguła, której nikt nie stosuje.

`/v1/me/*` nigdy nie może być pod uprawnieniem. Konfiguracja ról, która odcina kogoś od własnego konta, odcina też
jedyną osobę mogącą to naprawić.

## Dwa zakresy

`/v1/orgs/{orgID}/...` mierzy się względem jednej organizacji.
`/v1/platform/...` — względem całej instalacji, i to jedyne trasy bez `{orgID}`
w ścieżce.

Rozdział jest szczelny w obie strony i pokryty testami:

- właściciel każdej swojej organizacji **nie ma** nic na poziomie platformy;
- administrator platformy **nie ma** automatycznego wstępu do żadnej organizacji — dostaje 403 we własnej i 404 w
  cudzej.

Ta druga zasada wymaga drugiej połowy, której długo nie było. `create-platform-organization` celowo nie dodaje twórcy
(patrz `CreateOrganization`), a dodanie członka wymaga uprawnienia **wewnątrz** organizacji — którego administrator
platformy nie ma. Organizacja utworzona przez API nie miała więc nikogo i **nie dało się nikogo do niej dodać**; jedynym
wyjściem był SQL. `POST /v1/platform/organizations/{id}/owners` za `platform.organizations.owners.assign` jest tym
brakującym krokiem, i zarazem drogą powrotną z organizacji, która straciła ostatniego właściciela.

Wskazanie właściciela **nie nadaje roli instalacji**. Posiadanie jednej organizacji i administrowanie instalacją to
osobne autoryzacje z osobnymi endpointami — patrz [Role instalacji przez API](#role-instalacji-przez-api). Polecenie
bootstrap robi jedno i drugie, bo przerwanie koła wymaga obu, i przyjmuje teraz `-org`, więc nie jest już przywiązane do
organizacji `default`.

Parametr ścieżki w `/v1/platform/organizations/{id}` nazywa się `id`, nie
`orgID`. Middleware czyta `{orgID}` po to, żeby ustalić organizację, **w której**
autoryzuje; tutaj organizacja jest przedmiotem operacji, nie zakresem uprawnienia, a ta sama nazwa sugerowałaby, że to
jedno i to samo.

## Skąd bierze się organizacja

Ze **ścieżki**, nie z tokenu i nie z nagłówka.

- huma waliduje parametr ścieżki, więc nie da się go zapomnieć;
- ten sam kontekst widzi middleware i router, więc decyzja i operacja nie mogą dotyczyć różnych organizacji;
- „aktywna organizacja" w tokenie oznaczałaby, że przełączenie kontekstu wymaga nowego tokenu, a odebranie członkostwa
  działa dopiero po wygaśnięciu.

Middleware wkłada wynik decyzji do kontekstu jako `authz.Grant`. **Handler czyta organizację wyłącznie z grantu**, nigdy
z `in.OrgID` — pole w DTO istnieje tylko po to, żeby huma udokumentowała parametr. Gdyby handler czytał je ponownie,
mógłby zadziałać na innej organizacji niż ta, która przeszła autoryzację.
`TestHandlersDoNotReadTheOrgIDParameter` parsuje AST i przewraca build, jeśli którykolwiek handler się do niego odwoła.

## Uprawnień nie ma w tokenie

Świadomy koszt jednego zapytania na żądanie i bezpośrednia kontynuacja zasady, którą kod stosuje przy urządzeniach:
odebranie roli ma działać **teraz**, a nie po wygaśnięciu tokenu.

## Katalog uprawnień jest kodem

`internal/domain/authz/permission.go` jest źródłem prawdy. Nie ma tabeli
`permissions`: uprawnienie istnieje dokładnie wtedy, gdy jakiś handler go pilnuje, a wiersz w bazie nie może go powołać
do życia.

Konsekwencja jest obsłużona jawnie. Klucz usunięty z katalogu może zostać w
`role_permissions`; przy ewaluacji jest **ignorowany** (`authz.Sanitize`), więc nieaktualny wiersz nie nadaje niczego.
Endpointy ról raportują go jednak bez filtrowania — ekran ustawień musi go zobaczyć, żeby móc go usunąć.

Nazewnictwo: `[platform.]<zasób>[.<podzasób>].<akcja>`, akcja z zamkniętej listy
(`read/create/update/delete/assign/invite/remove/suspend`). **Żadnych wildcardów w bazie** — `*` istnieje tylko jako
skrót przy definiowaniu ról domyślnych w kodzie i jest rozwijany przy zapisie. Dopasowywanie wzorców w czasie ewaluacji
to miejsce, w którym mieszkają błędy autoryzacji.

Dodanie uprawnienia:
[instrukcja nowego uprawnienia](../guides/006_add_permission.md).

## Role domyślne

| Klucz            | Zakres      | Uprawnienia                                                       |
| ---------------- | ----------- | ----------------------------------------------------------------- |
| `platform_admin` | system      | wszystkie `platform.*` — wyprowadzone z katalogu                  |
| `owner`          | organizacja | **cały katalog organizacyjny** — wyprowadzony                     |
| `admin`          | organizacja | wszystko poza `organization.delete` — wypisane                    |
| `member`         | organizacja | `organization.read`, `members.read`, `files.read`, `files.create` |
| `viewer`         | organizacja | `organization.read`, `files.read`                                 |

`owner` jest wyprowadzany z katalogu, więc nowe uprawnienie trafia do niego w tym samym commicie i nie może stać się
funkcją, która nie działa dla nikogo i nie zgłasza powodu. `admin` celowo **nie** jest wyprowadzany: nowe uprawnienie
lądujące automatycznie u każdego administratora to cicha zmiana przywilejów dowieziona razem z niepowiązaną funkcją.

Role systemowe są materializowane per organizacja przy jej tworzeniu. Koszt:
migracja uzupełniająca przy rozszerzeniu katalogu — ta przy plikach dopisuje `files.*` do
istniejących `owner`/`admin`/`member`/`viewer`. Zysk: właściciel widzi dokładny skład roli `admin` i może ją
sklonować.

## Trzy drogi do eskalacji, jedna reguła

Najczęściej pomijana dziura: `roles.update` jest przechodnio uprawnieniem do wszystkiego, jeśli wolno dopisać do roli
coś, czego się nie ma.

Reguła: **można nadać wyłącznie uprawnienie, które samemu się posiada**
(`authz.EnsureCanGrant`). Obowiązuje we wszystkich trzech miejscach:

1. tworzenie roli,
2. zmiana tego, co rola nadaje,
3. przypisanie istniejącej roli komuś.

Trzecie sprawdza **uprawnienia roli**, nie jej identyfikator — inaczej ktoś, kto nie może nadać `organization.delete`,
nadałby je, wskazując rolę, która je zawiera.

Przed tą regułą działa kontrola zakresu: rola żyje w organizacji, więc klucz `platform.*` nie mógłby zostać przez nią
nadany **nigdy**, niezależnie od tego, co wołający ma. Bez tego odmowa wracała jako `403 privilege_escalation`, czyli
„brakuje ci uprawnienia" — i odsyłała klienta po coś, czego w tym zakresie nie da się mieć. Teraz jest to
`422 wrong_scope`. Osobny kod, a nie `unknown_permission`, bo katalog (`GET /v1/permissions`) oddaje każdy klucz **razem
z zakresem** — klient, który o niego prosi, przeczytał go od nas, więc „nieznany" byłoby po prostu nieprawdą.

## Reguła rangi

`EnsureCanGrant` pilnuje **nadawania**. Druga połowa dotyczy **odbierania** i bez niej schemat był egzekwowany tylko w
jedną stronę.

`members.remove`, `members.suspend` i `members.roles.assign` należą do roli `admin`, a członkostwo właściciela jest dla
każdego z nich zwykłym wierszem. Administrator mógł więc usunąć właściciela nad sobą, zawiesić go (czyli zamienić na
`404` we wszystkim) albo podmienić mu role na `viewer` — bo kontrola anty-eskalacyjna patrzy wyłącznie na role
**nadawane**, a `viewer` mieści się w zakresie admina. Efektem była inwersja: rola niższa neutralizuje wyższą. Hamowała
to jedynie reguła ostatniego właściciela, czyli przy dwóch właścicielach admin mógł jednego wyciąć.

Reguła: **nie wolno działać na członku, którego uprawnienia nie są podzbiorem twoich** (`authz.EnsureCanAffect`).
Obowiązuje w trzech miejscach: usunięcie, zmiana statusu, zmiana ról. Przy zmianie ról działają obie kontrole — najpierw
ranga (co cel _ma_), potem anty-eskalacja (co cel _dostanie_).

Szczegóły warte zapamiętania:

- **Działanie na sobie zawsze przechodzi.** Dziś przeszłoby i tak, bo grant jest wyprowadzany z ról wołającego w tej
  organizacji, więc jego własne członkostwo ma dokładnie ten sam zbiór. Wyjątek jest zapisany jawnie, żeby „usuń mnie z
  organizacji" nie zaczęło kiedyś zawodzić, gdy oba zbiory będą liczone z różnych miejsc.
- **Status celu nie ma znaczenia.** `MemberPermissions` sumuje uprawnienia ról członkostwa niezależnie od statusu.
  Zawieszony właściciel nie ma stać się usuwalny dla administratora tylko dlatego, że ktoś zawiesił go pierwszy.
- **Nieaktualny klucz jest pomijany, nie liczony.** `Sanitize` i tak go odrzuca przy ewaluacji, więc nie nadaje celowi
  niczego i nie może go chronić — inaczej jeden przestarzały wiersz w `role_permissions` czyniłby członka nietykalnym.
- **Konsekwencja dla zaproszeń:** administrator nie wycofa zaproszenia wystawionego przez właściciela na rolę `owner`.
  Zaproszone role są sprawdzane przy wystawianiu (`ensureRolesAreGrantable`), więc admin sam takiego nie wystawi, ale
  cudzego też nie cofnie. To spójne z regułą i zamierzone.
- **`403`, nie `404`.** Wołający może tego członka czytać, więc ukrywanie go byłoby i bezużyteczne, i mylące. Kod jest
  osobny od `privilege_escalation`, bo tu nic nie jest nadawane.
- Reguła rangi wyprzedza regułę ostatniego właściciela: „nie wolno ci tego w ogóle" jest ważniejsze niż „ta konkretna
  zmiana zepsułaby organizację".

## Statusy odmowy

| Sytuacja                                              | Status | `code`                        |
| ----------------------------------------------------- | ------ | ----------------------------- |
| brak/niepoprawny token                                | 401    | `unauthorized`                |
| członek bez uprawnienia                               | 403    | `forbidden_requires`          |
| organizacja nie istnieje **lub** nie jesteś członkiem | 404    | `not_found`                   |
| członkostwo zawieszone                                | 404    | `not_found`                   |
| zasób z cudzej organizacji                            | 404    | `not_found`                   |
| nadanie uprawnienia, którego się nie ma               | 403    | `privilege_escalation`        |
| uprawnienie z drugiego zakresu w roli                 | 422    | `wrong_scope`                 |
| zmiana dotyczy kogoś wyżej w hierarchii               | 403    | `insufficient_rank`           |
| zaproszenie wygasło                                   | 410    | `invitation_expired`          |
| zaproszenie na inny adres                             | 409    | `invitation_address_mismatch` |
| odebranie sobie ostatniej roli instalacji             | 409    | `last_system_role`            |
| klucz nie jest rolą instalacji                        | 422    | `invalid_system_role`         |
| edycja roli systemowej                                | 403    | `role_protected`              |
| ostatni właściciel                                    | 409    | `last_owner`                  |
| rola wciąż przypisana                                 | 409    | `role_in_use`                 |

Rozróżnienie 403/404 jest celowe i spójne z tym, co kod robi przy rejestracji (zduplikowany e-mail → 204). **403 mówi
„jesteś tu, ale nie wolno ci"; 404 nie zdradza, że coś istnieje.** W aplikacji wielonajemcowej różnica statusów zamienia
listę identyfikatorów w listę klientów.

Kształt odpowiedzi błędnej: [Błędy i języki](008_errors_and_i18n.md).

## Zawieszenie konta

`PATCH /v1/platform/users/{id}` z `{"suspended": true}` blokuje konto wszędzie.

Zamyka **trzy** drogi, nie jedną — i to jest cała lekcja z tej funkcji. Middleware odrzuca tokeny już wydane, ale
logowanie i weryfikacja drugiego składnika wydają **nowe**. Wersja pilnująca tylko pierwszej z nich nie była
zawieszeniem: konto po prostu logowało się ponownie.

Zawieszenie jest odwracalne i zachowuje członkostwa oraz role. Administrator nie może zawiesić ani usunąć samego siebie:
odebrałby sobie uprawnienie potrzebne do cofnięcia tej decyzji.

## Migawka dla frontendu

`GET /v1/me/permissions` zwraca wszystko, co wołający może zrobić, wszędzie.

Istnieje **wyłącznie po to, żeby UI wiedziało, co ukryć**. Nie jest mechanizmem egzekwowania: serwer podejmuje decyzję
od nowa przy każdym żądaniu, a klient ze starą migawką dostaje 403, który ma obsłużyć jako stan normalny — komunikat,
odświeżenie migawki, przerysowanie widoku. Nigdy ekran błędu krytycznego.

`ETag` jest haszem odpowiedzi, nie licznikiem. Licznik trzeba bezbłędnie inkrementować w każdej ścieżce zapisu;
pominięcie jednej daje nieświeże UI bez żadnego objawu. Hasz nie może się pomylić.

**`TestTheSnapshotAgreesWithEnforcement`** wiąże **migawkę** (`GET /v1/me/permissions`) z egzekwowaniem operacji
**organizacyjnych**: dla każdej chronionej operacji w zakresie organizacji pyta migawkę, czy wołający ma uprawnienie, a
potem faktycznie wywołuje endpoint. 403 musi wystąpić dokładnie wtedy, gdy migawka powiedziała „nie". Operacje
platformowe pilnuje `TestSystemScopeIsEnforcedEndToEnd` — ten sam wzorzec sondy, bez ETag.

## Audyt

`GET /v1/orgs/{orgID}/audit` za `audit.read`, `GET /v1/platform/audit` za
`platform.audit.read`.

**Wpis powstaje w tej samej transakcji co zmiana**, którą opisuje. To nie jest optymalizacja, tylko jedyny sposób, żeby
dziennik nie kłamał: zapis osobną instrukcją daje dwa tryby awarii — zmiana się cofa, a log twierdzi, że była, albo
zmiana wchodzi, a log jest pusty, bo druga instrukcja padła.

Aktor jedzie na **kontekście** (`audit.Actor`), nie jako parametr kilkunastu metod. To ta sama decyzja, którą kod podjął
dla request-scoped loggera i adresu klienta: to fakty o żądaniu, nie argumenty reguły biznesowej.

Bez aktora **nie powstaje żaden wiersz** — wpis, który nie mówi kto, jest gorszy niż jego brak, bo wygląda jak pokrycie.
Strażnikiem jest skutek, nie mechanizm:
`TestEveryMutatingOperationIsAudited` wywołuje po kolei każdy mutujący endpoint i wymaga wpisu. Ten sam test przewraca
build, gdy nowa chroniona operacja nie trafi ani na listę mutujących, ani tylko do odczytu.

Aktora ustawia `requireBearer`, więc domyślnie ma go **tylko** żądanie z tokenem. Jedna mutacja sięga do składowania bez
sesji: rejestracja, która dołącza do organizacji `default`. Przez to powstawało aktywne członkostwo bez żadnego śladu,
jak ktokolwiek się tam znalazł — a w organizacji `default` to najczęstszy sposób powstania członkostwa. Handler
rejestracji ustawia więc aktora sam, i jest nim **nowe konto**: to prawdziwy opis tego, co się stało (założenie konta
spowodowało członkostwo), a identyfikator wskazuje na istniejący wiersz, więc wpis renderuje się z nazwą, nie z gołym
uuid-em. Aktor tożsamy z podmiotem odróżnia ten wpis od wszystkich pozostałych.

Ta ścieżka ma własną akcję `member.joined`. `member.invited` opisywałoby zaproszenie, którego nikt nie wysłał, a
`member.accepted` — zgodę, której nikt nie wyraził. Tej samej akcji używa promowanie pierwszego właściciela: to również
provisioning, nie zaproszenie. Bootstrap uruchamiany z CLI nadal nie ma aktora i **nie zostawia wpisu** — patrz
[Bootstrap](#bootstrap).

Klucz roli i identyfikator konta zapisywane są **w momencie zmiany**, nie doklejane przy odczycie — rola może zostać
usunięta, a „kto usunął tę rolę" to dokładnie ten wpis, którego ktoś będzie szukał.

## Bootstrap

Pierwszego właściciela nie da się utworzyć przez API: żeby kogoś nim zrobić, trzeba mieć `members.roles.assign`, którego
nikt jeszcze nie ma. Koło przerywa polecenie poza API:

```bash
task bootstrap -- -email ada@example.com
task bootstrap -- -email ada@example.com -org seed-acme   # inna organizacja niż default
```

Flaga `-org` wybiera organizację po `slug` (domyślnie `default`). API może już nadawać właściciela w dowolnej
organizacji; bootstrap pozostaje dla przypadku, w którym nikogo z uprawnieniami jeszcze nie ma.

Nie „pierwszy zarejestrowany wygrywa" — przy otwartej rejestracji to wyścig, w którym może wziąć udział każdy.

`-platform-admin` jest **wyłączone domyślnie**. Flaga, która milcząco nadaje rolę instalacji, zamienia literówkę w
pierwszego administratora platformy; trzeba jej poprosić.

Organizacja `default` powstaje leniwie, przy pierwszej rejestracji, wraz ze swoimi rolami systemowymi. Jest
`IsProtected`, więc zwykła ścieżka usuwania jej odmawia. Nowe konto dołącza do niej jako `member`, a wpis w dzienniku
(`member.joined`) wskazuje jako aktora to właśnie konto — patrz [Audyt](#audyt).

Samo polecenie bootstrap nie ma sesji ani aktora, więc **nie zostawia wpisu w dzienniku**. Jest to świadome: reguła „bez
aktora nie ma wiersza" obowiązuje, a wpis przypisujący promowanie osobie, która przy tym nie była, byłby fałszem. Śladem
jest tu wykonanie polecenia na maszynie, nie dziennik aplikacji.

**Instalacja jednoorganizacyjna** nie wymaga niczego: wszyscy lądują w `default`, a model wielonajemcowy jest wtedy
niewidoczny — ale nie trzeba go dokładać później, gdy się okaże potrzebny.

### Role instalacji przez API

Bootstrap przerywa koło, ale nie może być jedyną drogą. Rola `platform_admin` obejmuje **wszystkie** klucze
`platform.*`, więc jej nadanie jest najbardziej doniosłą zmianą w instalacji — a przez długi czas nie zostawiało
**żadnego** śladu: nie było endpointu, `GrantSystemRole` nie wołało `record`, a dwie stałe akcji dla tego zdarzenia były
martwym kodem, podczas gdy [Audyt](#audyt) obiecywał, że każda zmiana uprawnień jest zapisywana.

| Operacja  | Ścieżka                                               | Uprawnienie                    |
| --------- | ----------------------------------------------------- | ------------------------------ |
| lista     | `GET /v1/platform/system-roles`                       | `platform.system_roles.read`   |
| nadanie   | `POST /v1/platform/system-roles`                      | `platform.system_roles.assign` |
| odebranie | `DELETE /v1/platform/system-roles/{userID}/{roleKey}` | `platform.system_roles.remove` |

Trzy uprawnienia, nie jedno: czytanie „kto administruje instalacją" to inna decyzja niż dopisanie się do tej listy.

Rzeczy, które trzeba znać:

- **Nadanie i odebranie są idempotentne, ale nie zapisują zdarzenia bez zmiany.** Wpis o nadaniu, które się nie odbyło,
  byłby drugą odpowiedzią na pytanie „kiedy to dostali". Bootstrap może zostać uruchomiony ponownie, więc idempotencja
  nie jest wygodą.
- **Nie ma tu anty-eskalacji, i to nie jest przeoczenie.** Zakresy są rozdzielone: wołający przeszedł autoryzację na
  poziomie systemu, a jedyna istniejąca rola systemowa obejmuje wszystkie uprawnienia platformowe — reguła „możesz nadać
  tylko to, co masz" porównywałaby zbiór ze sobą. Druga rola systemowa o węższym zbiorze to zmieni i wtedy właśnie tam
  trafi ta kontrola.
- **Nie da się odebrać sobie ostatniej.** Odebranie własnego `platform_admin` zabiera uprawnienie potrzebne do nadania
  go z powrotem, a bez innego posiadacza nie ma już nikogo, kto mógłby. `409 last_system_role`; przy drugim posiadaczu
  przechodzi, bo reguła **liczy**, a nie zabrania.
- **Klucz musi być rolą systemową.** Klucz roli organizacyjnej wylądowałby w `user_system_roles`, gdzie nic go nie
  czyta — `422 invalid_system_role`.
- **Grant z CLI nadal nie ma aktora**, więc nie zostawia wpisu. Ta sama zasada co przy bootstrapie i ten sam ślad:
  wykonanie polecenia na maszynie.

## Zaproszenia

Zaproszenie ma **własną tabelę** i **własny sekret**. Wcześniej było wierszem `memberships` ze `status='invited'`,
pustym
`user_id` i adresem na wierszu — czyli tożsamością zaproszenia był **adres**, a adres nie jest sekretem. Kto pierwszy
zarejestrował zaproszony adres, dziedziczył ofertę razem z rolami w organizacji, do której nigdy nie należał. Token
przenosi dowód z „twierdzę, że to mój adres" na „umiem przeczytać tę skrzynkę".

| Kolumna       | Po co                                                                          |
| ------------- | ------------------------------------------------------------------------------ |
| `email`       | dokąd wysłano; unikalna w organizacji, wciąż porównywana przy przyjęciu        |
| `token_hash`  | jedyna kopia tokenu po tej stronie                                             |
| `expires_at`  | oferta bez wygaśnięcia to poświadczenie leżące bezterminowo w skrzynce         |
| `accepted_at` | wydane; wiersz zostaje, żeby historia „komu co oferowano" przeżyła członkostwo |

`invitation_roles` odbija `membership_roles`: role muszą przejść od oferty do członkostwa **bez ponownego wyboru** —
przyjmujący nie może decydować, co przyjmuje.

Po stronie `memberships` zniknęły **wszystkie** ślady dawnego rozwiązania: status `invited`, kolumna `email` i
nullowalny `user_id`. Każde członkostwo ma teraz konto, więc join do `users` może być wewnętrzny, a unikat
`(user_id, organization_id)` znów działa — przy nullowalnej kolumnie nie działał, bo Postgres traktuje dwa `NULL`-e jako
różne. Zniknęła też maszyneria savepointów w `AddMember`, która istniała wyłącznie po to, żeby provisioning nie odbijał
się od zaproszenia na ten sam adres.

**Hash to zwykłe `SHA-256`, bez pepperu**, i to celowa różnica wobec kodów sześciocyfrowych. Tamte mają mało entropii i
potrzebują sekretu, żeby zgadywanie offline było drogie. Tu są 32 losowe bajty — nie ma czego zgadywać, a klucz byłby
tylko kolejnym sekretem do zgubienia. Odciski urządzeń są hashowane tak samo i z tego samego powodu.

`POST /v1/orgs/{orgID}/members` **nie szuka adresu w tabeli kont**. Znany i nieznany adres dają ten sam `201` i ten sam
kształt odpowiedzi. Szukanie byłoby oraklem rejestracji: fakt, że ktoś ma konto, jest faktem **międzyorganizacyjnym**, a
administrator organizacji nie jest do niego uprawniony. `ErrAlreadyMember` obsługuje jednocześnie „już jest członkiem"
i „już ma zaproszenie" — wołający reaguje na jedno i drugie tak samo.

**Odpowiedź nie zawiera tokenu.** Administrator, który mógłby go odczytać, mógłby przyjąć zaproszenie za zaproszonego —
czyli dokładnie to, co token miał zlikwidować. Token istnieje w tym procesie w jednym momencie i idzie do maila.

### Cykl życia po stronie organizacji

| Operacja             | Ścieżka                                          | Uprawnienie      |
| -------------------- | ------------------------------------------------ | ---------------- |
| zaproszenie          | `POST /v1/orgs/{orgID}/members`                  | `members.invite` |
| zaproszenie zbiorcze | `POST /v1/orgs/{orgID}/invitations`              | `members.invite` |
| lista                | `GET /v1/orgs/{orgID}/invitations`               | `members.read`   |
| ponowne wysłanie     | `POST /v1/orgs/{orgID}/invitations/{id}/reissue` | `members.invite` |
| wycofanie            | `DELETE /v1/orgs/{orgID}/invitations/{id}`       | `members.invite` |

Te trzy istnieją, bo zaproszenie **wypadło z listy członków**. Dopóki było wierszem `memberships`, administrator widział
je i wycofywał przez `remove-member`; usunięcie go stamtąd bez dania niczego w zamian zostawiłoby ofertę, której nikt
nie widzi i nie może odwołać.

### Zaproszenie zbiorcze

`POST /v1/orgs/{orgID}/invitations` — jeden zestaw ról, do **50** adresów, jedno żądanie.

Powstało z dwóch powodów: onboarding zespołu po jednym żądaniu na osobę wywracał się na limiterze, a każde z tych żądań
różniło się wyłącznie adresem.

**Każdy adres ma własny wynik** (`invited` albo `already_member`), a nie cała paczka jeden status. Administrator, który
wkleja dwunastu kolegów, z których dwóch już jest w organizacji, chce wysłanych dziesięciu zaproszeń — „wszystko albo
nic" kazałoby mu szukać tych dwóch bisekcją. **Nie ma transakcji wokół paczki** i to jest sens listy wyników.

Wyniki dopasowuje się **po adresie, nie po pozycji**.

Role są sprawdzane **raz, przed czymkolwiek zapisanym**: są te same dla wszystkich adresów, więc próba nadania więcej,
niż wołający ma, jest odmową całego żądania, a nie pięćdziesięcioma identycznymi odmowami.

Implementacja woła `Invite` w pętli, zamiast wstawiać zbiorczo: każde zaproszenie potrzebuje **własnego losowego
tokenu**, własnego wiersza i własnego wpisu w audycie, a wstawka zbiorcza musiałaby odtworzyć wszystkie trzy rzeczy.

Nieznany adres jest zapraszany tak samo jak znany — ta operacja też nie jest oraklem rejestracji.

### Limiter: własny budżet

Zaproszenia mają `INVITE_PER_MINUTE` (domyślnie 30) zamiast dzielić `REGISTER_PER_MINUTE` (5). Wspólny budżet miał sens,
gdy `add-member` był oraklem rejestracji; po przejściu na tokeny uzasadnienie zostało tylko takie, że „to też wysyłka
maila", a liczba została ta sama i onboarding zespołu z jednego biurowego adresu kończył się na piątej osobie.

Na tym samym budżecie jest **ponowne wysłanie** — robi to samo, czyli nowy token na ten sam adres. Wcześniej nie było
limitowane **wcale**, i to jest ciekawsza połowa tej zmiany: switch limitera dopasowuje ścieżki **literalnie**, więc
trasa wysyłająca maile po prostu w nim nie występowała i nic tego nie zgłaszało.
`TestRateLimitAppliesToEveryCostlyRoute` wymienia teraz obie.

**Znane ograniczenie, zapisane świadomie:** jedno żądanie to jeden token limitera, niezależnie od długości listy, więc
realna granica to `INVITE_PER_MINUTE` × 50 adresów. Limiter jest middleware chi i działa **przed** odczytaniem ciała,
więc nie umie policzyć adresów bez parsowania żądania dwa razy. Uczciwym rozwiązaniem jest limit wysyłki **per
organizacja**, a nie kubełek per IP — tym bardziej że na trasie uwierzytelnionej IP jest słabszym przybliżeniem sprawcy
niż na anonimowej.

**Cały cykl życia oferty stoi za `members.invite`** — wysłanie, ponowne wysłanie i wycofanie. Wycofanie było jedyne za
`members.remove`, co dawało dwie niekonsekwencje naraz: trzeci krok jednego cyklu za innym uprawnieniem niż dwa
pierwsze, i naprawienie własnej literówki w adresie drożej niż jej popełnienie. `members.remove` znaczy teraz jedno:
**odbierz komuś dostęp**. Wycofanie oferty nikomu dostępu nie odbiera, bo nikt go jeszcze nie ma.

Żadna rola shipowana tego nie rozróżniała (`owner` i `admin` mają oba uprawnienia), więc zmiana dotyczy wyłącznie ról
własnych: rola z samym `members.invite` zaczyna móc wycofywać, rola z samym `members.remove` przestaje.

**Ponowne wysłanie wymienia token** i przesuwa wygaśnięcie. Nie „wysyła tego samego jeszcze raz" — to utrzymywałoby
wyciekniętą wiadomość ważną kolejny tydzień — i nie tworzy drugiego zaproszenia, bo zderzyłoby się z unikatem
`(organization_id, email)`. Stary link przestaje działać, i to jest cel.

**Wycofanie i odrzucenie to dwie operacje**, choć robią wierszowi to samo. Różnią się tym, **co je autoryzuje**:
zaproszony ma token, organizacja ma `members.invite`. Zlanie ich w jedną znaczyłoby, że jedna z tych autoryzacji
zastępuje drugą. Dziennik też je rozróżnia (`member.invitation_withdrawn` vs `member.invitation_declined`), bo „kto to
zakończył" jest dokładnie tym pytaniem, na które wpis odpowiada.

### Przyjęcie: dwa warunki, dwa różne pytania

| Operacja   | Ścieżka                           | Kategoria     |
| ---------- | --------------------------------- | ------------- |
| lista      | `GET /v1/me/invitations`          | samoobsługowa |
| przyjęcie  | `POST /v1/me/invitations/accept`  | samoobsługowa |
| odrzucenie | `POST /v1/me/invitations/decline` | samoobsługowa |

Token jedzie w **ciele**, nie w ścieżce: token w URL-u ląduje w logach dostępu, w historii przeglądarki i w nagłówku
`Referer` tego, co strona załaduje dalej. Kody resetu hasła są przyjmowane w ciele z tego samego powodu.

1. **Token** dowodzi, że wołający dostał wiadomość. To on zastąpił „kto pierwszy zarejestruje ten adres".
2. **Adres konta musi się zgadzać** z adresem oferty — węższa reguła wybrana w D4. Trzyma ofertę skierowaną na osobę,
   dla której była, a nie na tego, komu wiadomość przekazano.

Adres jest czytany z **konta**, nie z żądania. Z ciała pozwoliłby wskazać cudzy adres, z tokenu usunąłby drugi warunek w
całości.

Statusy odmowy są rozróżnione, bo wołający **trzyma token** — istnienie zaproszenia nie jest przed nim tajemnicą, a goły
`404` nie dałby mu nic, co mógłby powiedzieć osobie zapraszającej:

| Sytuacja       | Status | `code`               |
| -------------- | ------ | -------------------- |
| token nieznany | 404    | `not_found`          |
| oferta wygasła | 410    | `invitation_expired` |

## Wyjście z organizacji

| Operacja           | Ścieżka                                      | Kategoria        |
| ------------------ | -------------------------------------------- | ---------------- |
| opuszczenie własne | `DELETE /v1/me/memberships/{membershipID}`   | samoobsługowa    |
| usunięcie kogoś    | `DELETE /v1/orgs/{orgID}/members/{memberID}` | `members.remove` |

Dwie operacje, bo różni je **co je autoryzuje** — dokładnie jak przy wycofaniu i odrzuceniu zaproszenia. Do niedawna
istniała tylko druga, więc jedyne wyjścia to była prośba do administratora albo `remove-member` na sobie samym, co
wymaga `members.remove`: wołający najbardziej zainteresowani wyjściem — ci bez żadnych uprawnień — byli jedynymi, którzy
nie mogli. Uprawnienie za tą operacją znaczyłoby, że organizacja może skonfigurować rolę, która nie potrafi odejść.

**Ścieżka nazywa członkostwo, nie organizację.** `{orgID}` w ścieżce znaczy „middleware rozstrzygnął tu uprawnienie" i
ma znaczyć tylko to; `TestHandlersDoNotReadTheOrgIDParameter` pilnuje, że handler nigdy nie czyta tego parametru z
requestu. Trasa samoobsługowa z `{orgID}` byłaby pierwszym wyjątkiem od tej reguły — a identyfikator członkostwa i tak
przychodzi z `GET /v1/me/organizations`, więc klient nic nie traci.

Autoryzacją jest to, że **członkostwo znajduje się na własnej liście wołającego**. Cudze `id` daje 404, nie 403: 403
potwierdziłoby, że taki wiersz istnieje. Nic w tej ścieżce nie bierze identyfikatora organizacji z requestu, więc nie ma
kształtu, w którym trafiłby on do zawężonej metody repozytorium bez sprawdzenia.

**Reguła ostatniego właściciela obowiązuje bez zmian** — 409 z kodem `last_owner`. Ktoś musi móc administrować
organizacją, a „odchodzę" nie jest powodem do wyjątku; odmowa mówi wołającemu, żeby najpierw wskazał innego właściciela,
i to jest rzecz, którą może zrobić.

Dziennik rozróżnia oba wyjścia: `member.left` i `member.removed`. Czytający mógłby je rozpoznać porównując aktora z
podmiotem, ale „Ada usunęła Adę z organizacji" nie jest tym, co się stało. To ta sama para co
`member.invitation_declined` i `member.invitation_withdrawn`.

Akcja jest **parametrem** `RemoveMember` z tego samego powodu, z jakiego `guard` jest callbackiem: usunięcie wiersza
wygląda w store identycznie, a tylko wołający wie, czy administrator kogoś usunął, czy ktoś odszedł. Store odpowiada za
moment zapisu, domena za jego znaczenie.

## Organizacja bez właściciela

Naprawianie istniało od H2 — `POST /v1/platform/organizations/{id}/owners` wskazuje właściciela z zewnątrz, bez
dołączania wołającego. **Brakowało wykrywania**: administrator instalacji musiałby już wiedzieć, której organizacji
szukać, a to nie jest rzecz, którą się wie o czyimś tenancie.

`GET /v1/platform/organizations` niesie teraz w każdym wierszu `owners`, a `?without_owner=true` zawęża listę do tych,
którymi nikt nie może administrować. Dwie drogi do zera:

1. organizacja utworzona przez platformę i jeszcze nieobsadzona — tworzenie **świadomie** nie dodaje twórcy,
2. właściciel, którego **konto zostało usunięte** — wiersz członkostwa przeżywa osobę i przestaje się liczyć wszędzie
   tam, gdzie to ma znaczenie.

**Liczenie jest jedno.** Podzapytanie `activeOwners` w SQL i `ownerStateTx`, z którego czyta reguła ostatniego
właściciela, mają tę samą definicję: aktywne członkostwo z rolą `owner` i **żywym kontem**. Dwie odpowiedzi na pytanie
„czy ta organizacja ma właściciela" w końcu by się rozjechały, a rozjazd wyglądałby tak, że lista pokazuje właściciela
dla organizacji, którą reguła uważa za pozbawioną właścicieli. Przypina to
`TestTheOwnerCountAgreesWithTheOwnerRule` — czyta liczbę z listy i **z samego guarda**, i sprawdziłem, że nie
przechodzi, gdy z podzapytania wypadnie warunek o usuniętym koncie.

Pole `owners` jest tylko na odpowiedzi platformowej. Ilu właścicieli mają inne tenanty instalacji, nie jest sprawą
członka.

## Reguły transakcyjne: gdzie mieszka decyzja

Reguła ostatniego właściciela sprawdza i mutuje **w jednej transakcji**, z `SELECT … FOR UPDATE` na wierszu organizacji.
Dwa nakładające się zdegradowania nie mogą oba zobaczyć `owners > 1` i oba przejść.

Sama reguła jest jednak **kodem domenowym**, nie SQL-em. Mieszkała w zapytaniu i w ręcznie pisanej kopii w fake'u — i
tak właśnie doszło do rozjechania się obu: jedna liczyła członkostwo z usuniętym kontem jako właściciela, druga nie, a
wiersz stawał się nieusuwalny. Dziś jest jedno sformułowanie, w Go:

```go
orgs.RefuseLastOwnerLoss(losing bool) OwnerGuard
orgs.RefuseRoleInUse() RoleGuard
```

Repozytorium przyjmuje **strażnika** i woła go wewnątrz transakcji, po zablokowaniu wiersza organizacji, przekazując
policzone fakty (`OwnerState{Owners, SubjectHoldsOwner}` albo liczbę posiadaczy roli). Rozkład jest taki:

| Co                             | Gdzie           |
| ------------------------------ | --------------- |
| decyzja („czy odmówić")        | domena (`orgs`) |
| transakcja, blokada, zliczenie | repozytorium    |

Świadomie **nie** wprowadzono osobnego interfejsu `orgs.Tx` z metodami mutującymi. Przeniósłby do domeny także zapisy,
czyli zduplikował kilkanaście metod repozytorium w drugim interfejsie, który fake musiałby odtworzyć — dużo ryzyka przy
przenoszeniu reguły bezpieczeństwa i żadnego zysku ponad to, co daje strażnik.

Trzy szczegóły, które trzymają to razem:

- **Strażnik jest wymagany, nie opcjonalny.** Zmiana, która nie może stracić właściciela, przekazuje
  `RefuseLastOwnerLoss(false)`, a nie `nil`. Nie ma więc gałęzi, w której blokada jest po cichu pomijana.
- **Wszystkie serializowane zmiany blokują ten sam obiekt** — wiersz organizacji. Dotyczy to również usuwania roli, co
  jest właśnie tym, co porządkuje je względem równoległego przypisywania roli. Dwie różne blokady brane w dwóch
  kolejnościach to przepis na zakleszczenie.
- **`RefuseLastOwnerLoss` jest eksportowane**, bo testy warstwy składowania muszą sprawdzać **prawdziwą** regułę na
  prawdziwym SQL-u. Test z własną kopią porównania mógłby przechodzić, gdy reguła serwisu mówi co innego — czyli
  dokładnie ta awaria, której cała ta zmiana ma zapobiegać.

`OwnerCount` jako metoda repozytorium **została usunięta**. Liczba istnieje teraz tylko wewnątrz transakcji; osobna
metoda do liczenia z puli byłaby drugą odpowiedzią na to samo pytanie, a dwie odpowiedzi w końcu się rozjeżdżają.

## Rejestracja nie jest przyjęciem

`POST /v1/users` tworzy konto i dołącza je do organizacji `default` jako zwykłego członka. **Nie przyjmuje** oczekujących
zaproszeń na ten adres — zaproszenia czekają na `POST /v1/me/invitations/{id}/accept`.

Powód jest prosty: adres na nowym kontie **nie jest zweryfikowany**. Rejestracja to „ktoś wpisał ten adres i zna hasło",
a nie „ta osoba czyta skrzynkę". Gdyby rejestracja automatycznie przyjmowała zaproszenia, pierwszy, kto zarejestruje
zaproszony adres, dziedziczył rolę w organizacji, do której nigdy nie należał. Wcześniej istniała metoda
`AcceptInvitationsByEmail` — została usunięta właśnie po to, żeby ten kanał nie wrócił.

Ta zasada łączy się z brakiem weryfikacji e-mail przy signup — patrz [Czego tu nie ma](#czego-tu-nie-ma).

## Czego tu nie ma

- **Weryfikacja adresu przy rejestracji.** Brak kolumny `verified_at` i brak flow „potwierdź skrzynkę po signup": adres
  na koncie jest niepotwierdzony przez cały czas jego życia. Zmiana adresu to osobny flow (`POST /v1/me/email` → kod na
  nowy adres → `POST /v1/me/email/verify`); to nie jest weryfikacja konta przy rejestracji. Na tym opiera się
  [decyzja, że rejestracja nie jest przyjęciem](#rejestracja-nie-jest-przyjęciem).
- **Tłumaczenia ról własnych.** Świadomie nie ma. Nazwa roli utworzonej przez klienta jest pokazywana tak, jak ją
  wpisał — jest już w języku, w którym pracuje. Tabela `role_translations` na drugą odpowiedź istniała bez czytelnika i
  pisarza; została usunięta. Nazwy ról **shipowanych** są tłumaczone, z katalogu, po `Key`.
- **Cache uprawnień.** Rozwiązanie uprawnień to jedno zapytanie, migawka też. Cache dokłada ryzyko nieświeżej decyzji za
  oszczędność, której nikt jeszcze nie zmierzył.
- **TOCTOU na grancie.** Decyzja zapada raz, w middleware; handler pracuje na `Grant` z kontekstu. Zmiana roli w trakcie
  żądania nie cofa trwającej operacji. Akceptowalne i warte zapisania, nie warte rozproszonej blokady.
