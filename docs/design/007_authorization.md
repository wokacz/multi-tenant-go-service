# Autoryzacja

Uwierzytelnianie odpowiada na pytanie „kim jesteś". Ten dokument jest o drugim pytaniu: **„co ci wolno"**.

## Dwa pytania, dwa miejsca

Decyzja rozkłada się na dwie połowy, rozstrzygane w różnych warstwach. To jest sedno projektu, nie szczegół
implementacji.

| Pytanie                                           | Gdzie                                                    | Jak                                                           |
|---------------------------------------------------|----------------------------------------------------------|---------------------------------------------------------------|
| Czy wołający ma uprawnienie X w organizacji O?    | middleware `requirePermission` (`internal/api/authz.go`) | mapa `operationAccess` + `authz.Authorize`                    |
| Czy zasób, na który działa, leży w organizacji O? | repozytorium                                             | `orgID` jest **obowiązkowym drugim parametrem** każdej metody |

Druga połowa jest strukturalna. Nie da się pobrać roli inaczej niż przez
`Role(ctx, orgID, roleID)`, więc pominięcie kontroli zasięgu jest błędem kompilacji, a nie dziurą, którą ktoś musi
wypatrzeć na review.
`TestScopedRepositoryMethodsTakeAnOrganization` pilnuje tego kształtu.

## Trzy rozłączne kategorie operacji

Rozszerzenie wzorca, który kod stosował już przy `publicOperations`.

| Kategoria     | Zbiór                   | Znaczenie                                      |
|---------------|-------------------------|------------------------------------------------|
| publiczna     | `publicOperations`      | bez tokenu                                     |
| samoobsługowa | `selfServiceOperations` | token wystarcza — tożsamość *jest* autoryzacją |
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

| Klucz            | Zakres      | Uprawnienia                                      |
|------------------|-------------|--------------------------------------------------|
| `platform_admin` | system      | wszystkie `platform.*` — wyprowadzone z katalogu |
| `owner`          | organizacja | **cały katalog organizacyjny** — wyprowadzony    |
| `admin`          | organizacja | wszystko poza `organization.delete` — wypisane   |
| `member`         | organizacja | `organization.read`, `members.read`              |
| `viewer`         | organizacja | `organization.read`                              |

`owner` jest wyprowadzany z katalogu, więc nowe uprawnienie trafia do niego w tym samym commicie i nie może stać się
funkcją, która nie działa dla nikogo i nie zgłasza powodu. `admin` celowo **nie** jest wyprowadzany: nowe uprawnienie
lądujące automatycznie u każdego administratora to cicha zmiana przywilejów dowieziona razem z niepowiązaną funkcją.

Role systemowe są materializowane per organizacja przy jej tworzeniu. Koszt:
migracja uzupełniająca przy rozszerzeniu katalogu. Zysk: właściciel widzi dokładny skład roli `admin` i może ją
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

`EnsureCanGrant` pilnuje **nadawania**. Druga połowa dotyczy **odbierania** i bez niej schemat był egzekwowany tylko
w jedną stronę.

`members.remove`, `members.suspend` i `members.roles.assign` należą do roli `admin`, a członkostwo właściciela jest dla
każdego z nich zwykłym wierszem. Administrator mógł więc usunąć właściciela nad sobą, zawiesić go (czyli zamienić na
`404` we wszystkim) albo podmienić mu role na `viewer` — bo kontrola anty-eskalacyjna patrzy wyłącznie na role
**nadawane**, a `viewer` mieści się w zakresie admina. Efektem była inwersja: rola niższa neutralizuje wyższą. Hamowała
to jedynie reguła ostatniego właściciela, czyli przy dwóch właścicielach admin mógł jednego wyciąć.

Reguła: **nie wolno działać na członku, którego uprawnienia nie są podzbiorem twoich** (`authz.EnsureCanAffect`).
Obowiązuje w trzech miejscach: usunięcie, zmiana statusu, zmiana ról. Przy zmianie ról działają obie kontrole — najpierw
ranga (co cel *ma*), potem anty-eskalacja (co cel *dostanie*).

Szczegóły warte zapamiętania:

- **Działanie na sobie zawsze przechodzi.** Dziś przeszłoby i tak, bo grant jest wyprowadzany z ról wołającego w tej
  organizacji, więc jego własne członkostwo ma dokładnie ten sam zbiór. Wyjątek jest zapisany jawnie, żeby „usuń mnie
  z organizacji" nie zaczęło kiedyś zawodzić, gdy oba zbiory będą liczone z różnych miejsc.
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

| Sytuacja                                              | Status | `code`                 |
|-------------------------------------------------------|--------|------------------------|
| brak/niepoprawny token                                | 401    | `unauthorized`         |
| członek bez uprawnienia                               | 403    | `forbidden_requires`   |
| organizacja nie istnieje **lub** nie jesteś członkiem | 404    | `not_found`            |
| członkostwo zawieszone                                | 404    | `not_found`            |
| zasób z cudzej organizacji                            | 404    | `not_found`            |
| nadanie uprawnienia, którego się nie ma               | 403    | `privilege_escalation` |
| uprawnienie z drugiego zakresu w roli                  | 422    | `wrong_scope`          |
| zmiana dotyczy kogoś wyżej w hierarchii               | 403    | `insufficient_rank`    |
| edycja roli systemowej                                | 403    | `role_protected`       |
| ostatni właściciel                                    | 409    | `last_owner`           |
| rola wciąż przypisana                                 | 409    | `role_in_use`          |

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

**`TestTheSnapshotAgreesWithEnforcement`** to jedyny test gwarantujący, że front i backend się nie rozjadą: dla każdej
chronionej operacji pyta migawkę, czy wołający ma uprawnienie, a potem faktycznie wywołuje endpoint. 403 musi wystąpić
dokładnie wtedy, gdy migawka powiedziała „nie".

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
```

Nie „pierwszy zarejestrowany wygrywa" — przy otwartej rejestracji to wyścig, w którym może wziąć udział każdy.

`-platform-admin` jest **wyłączone domyślnie**. Flaga, która milcząco nadaje rolę instalacji, zamienia literówkę w
pierwszego administratora platformy; trzeba jej poprosić.

Organizacja `default` powstaje leniwie, przy pierwszej rejestracji, wraz ze swoimi rolami systemowymi. Jest
`IsProtected`, więc zwykła ścieżka usuwania jej odmawia. Nowe konto dołącza do niej jako `member`, a wpis w dzienniku
(`member.joined`) wskazuje jako aktora to właśnie konto — patrz [Audyt](#audyt).

Samo polecenie bootstrap nie ma sesji ani aktora, więc **nie zostawia wpisu w dzienniku**. Jest to świadome: reguła „bez
aktora nie ma wiersza" obowiązuje, a wpis przypisujący promowanie osobie, która przy tym nie była, byłby fałszem.
Śladem jest tu wykonanie polecenia na maszynie, nie dziennik aplikacji.

**Instalacja jednoorganizacyjna** nie wymaga niczego: wszyscy lądują w `default`, a model wielonajemcowy jest wtedy
niewidoczny — ale nie trzeba go dokładać później, gdy się okaże potrzebny.

## Zaproszenia

`POST /v1/orgs/{orgID}/members` **nie szuka adresu w tabeli kont**. Zapisuje członkostwo `status=invited` z pustym
`user_id` i adresem na wierszu. Znany i nieznany adres dają ten sam `201` i ten sam kształt odpowiedzi (`user_id` oraz
`name` są nieobecne), więc administrator nie może pytać całej instalacji „czy ta osoba ma tu konto".

Szukanie adresu byłoby oraklem rejestracji: fakt, że ktoś ma konto, jest faktem **międzyorganizacyjnym**, a
administrator organizacji nie jest do niego uprawniony. Unikalność `(organization_id, email)` odmawia ponownego
zaproszenia **w tej** organizacji — to już jest fakt, który widać na liście członków.

Ścieżka provisioningu (`AddMember` w repozytorium: bootstrap, dołączenie do `default`) nadal tworzy od razu `active` z
`user_id`, bo tam konto już istnieje i nie ma czego ukrywać.

Po zapisie handler wysyła mail. Awaria SMTP ląduje w logu; HTTP i tak kończy się `201` — inaczej administrator, który
nie odróżni „adres już w organizacji" od „poczta nie wyszła", wpisałby go ponownie i dostał `409`.

Zaproszony przyjmuje albo odrzuca **sam**, bo zgoda nie może być zastąpiona przez `PATCH` administratora:

| Operacja   | Ścieżka                                         | Kategoria     |
|------------|-------------------------------------------------|---------------|
| przyjęcie  | `POST /v1/me/invitations/{invitationID}/accept` | samoobsługowa |
| odrzucenie | `DELETE /v1/me/invitations/{invitationID}`      | samoobsługowa |

Ścieżka jest pod `/v1/me`, nie pod `{orgID}`: zaproszony często nie ma jeszcze członkostwa, które middleware mogłoby
zautoryzować, a cudze zaproszenie jest nieodróżnialne od brakującego (`404`).

### Rejestracja nie jest przyjęciem

Rejestracja **nie aktywuje** zaległych zaproszeń na podany adres. Robiła to wcześniej — nowe konto lądowało od razu we
wszystkich organizacjach, do których zaproszono ten adres — i to była dziura, nie wygoda: **adres konta nie jest
weryfikowany** (patrz [Czego tu nie ma](#czego-tu-nie-ma)), więc rejestracja nie dowodzi niczego o skrzynce. Kto
pierwszy zarejestrował zaproszony adres, dziedziczył zaproszenie razem z rolami w cudzej organizacji — a prawowity
adresat nie mógł się już zarejestrować, bo adres był zajęty, i widział `204` jak przy sukcesie.

Zaproszenie przyjmuje wyłącznie zaproszony, przez `POST /v1/me/invitations/{invitationID}/accept`.

Z tego wynika drugi, mniej oczywisty warunek. Rejestracja dołącza jeszcze do organizacji `default`, a ta ścieżka idzie
przez `AddMember`, który przy kolizji na unikacie `(organization_id, email)` **przejmuje** zaproszenie: aktywuje
członkostwo i podmienia nadane role na `member`. Samo usunięcie automatycznego przyjęcia zostawiłoby więc dziurę otwartą
dla organizacji `default`, i to z cichą degradacją ról. Dlatego `JoinDefaultOrganization` najpierw sprawdza, czy konto
ma już jakikolwiek wiersz w `default` — w tym zaproszenie adresowane na jego adres — i wtedy nie robi nic.

Konsekwencja, którą trzeba znać: konto zaproszone do `default` po rejestracji **nie ma żadnego aktywnego członkostwa**,
dopóki nie przyjmie zaproszenia. To jest poprawne — samoobsługa (`/v1/me/*`) działa bez członkostwa, a alternatywą jest
odebranie zaproszonemu tego, co mu zaoferowano.

Przejmowanie zaproszenia w `AddMember` **zostaje**, bo obsługuje ścieżkę operatora działającego poza API (bootstrap,
wskazanie pierwszego właściciela): bez niego konto, które ktoś zdążył zaprosić, nie dałoby się promować, a zaproszenia
nie ma jak wycofać, dopóki nikt nie ma uprawnienia w tej organizacji.

Zostaje wąskie okno: zaproszenie utworzone **pomiędzy** sprawdzeniem a wstawieniem wiersza nadal zostanie przejęte.
Skutkiem jest degradacja ról, nie eskalacja, i zamknie się dopiero wtedy, gdy zaproszenia dostaną własną tabelę i
przestaną dzielić unikat z członkostwami.

Reguła ostatniego właściciela sprawdza i mutuje **w jednej transakcji**, z `SELECT … FOR UPDATE` na wierszu organizacji.
Dwa nakładające się zdegradowania nie mogą oba zobaczyć `owners > 1` i oba przejść.

## Czego tu nie ma

- **Weryfikacja adresu e-mail.** Nie ma ani kolumny, ani endpointu: adres na koncie jest niepotwierdzony przez cały czas
  jego życia. Dlatego adres **nie może być czynnikiem, który cokolwiek nadaje** — na tym opiera się decyzja, że
  [rejestracja nie jest przyjęciem](#rejestracja-nie-jest-przyjęciem).
- **Tłumaczenia ról własnych.** Tabela `role_translations` istnieje, ale nic jej nie czyta ani nie zapisuje. Nazwy ról
  tworzonych przez użytkownika pokazują się z kolumny `roles.name`, w jednym języku.
- **Cache uprawnień.** Rozwiązanie uprawnień to jedno zapytanie, migawka też. Cache dokłada ryzyko nieświeżej decyzji za
  oszczędność, której nikt jeszcze nie zmierzył.
- **TOCTOU na grancie.** Decyzja zapada raz, w middleware; handler pracuje na `Grant` z kontekstu. Zmiana roli w trakcie
  żądania nie cofa trwającej operacji. Akceptowalne i warte zapisania, nie warte rozproszonej blokady.
