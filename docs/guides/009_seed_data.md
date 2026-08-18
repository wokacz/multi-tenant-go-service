# Dane rozwojowe (seeder)

Wypełnia bazę **rozwojową** danymi, na których da się pracować: udokumentowaną obsadą kont, setką dodatkowych osób do
przewijania i organizacjami w każdym z kształtów, o których kod ma regułę — łącznie z taką, której API **nie umie
utworzyć**.

```bash
task seed              # dosyp to, czego brakuje
task seed -- -reset    # najpierw usuń dane seedera
task seed -- -only=cast
```

Hasło wszystkich kont: **`seed-password`**. Wszystkie adresy są w domenie **`seed.test`** — zarezerwowanej przez RFC
6761, więc nigdy nie będzie prawdziwą skrzynką. To nie kosmetyka: na tym opiera się trzecia warstwa zabezpieczenia.

## Zabezpieczenia

Trzy warstwy i **nie są tym samym rodzajem rzeczy**:

| Warstwa                         | Kiedy odmawia               | Da się pominąć?                                |
|---------------------------------|-----------------------------|------------------------------------------------|
| `ENV=production`                | zawsze                      | **nie**, żadną flagą                           |
| brak `-yes`                     | zawsze                      | flaga `-yes` (`task seed` podaje ją za Ciebie) |
| baza ma konto spoza `seed.test` | gdy ktoś ma tam własne dane | flaga `-force`                                 |

Pierwsza jest twarda, bo seeder wpisuje znane hasło na każde utworzone konto i jedno **usuwa celowo**. Pozostałe dwie
chronią przed pomyłką, a nie przed katastrofą — a bezpiecznik bez wyjścia awaryjnego kończy się skopiowaniem kodu w inne
miejsce.

W praktyce jest jeszcze czwarta, niezamierzona: konfiguracja produkcyjna i tak nie wstanie z hasłem `postgres` ani z
`POSTGRES_SSL_MODE=disable`, więc seeder nie dojdzie nawet do własnej odmowy.

## Obsada

Każde konto to **sytuacja**, o której kod ma regułę. Chodzi o to, żeby zalogować się w przypadek, a nie budować go
ręcznie.

| Konto                 | Nazwa            | Sytuacja                                                                                                    |
|-----------------------|------------------|-------------------------------------------------------------------------------------------------------------|
| `platform@seed.test`  | Pola Platformowa | Installation administrator: platform_admin, plus owner of seed-acme                                         |
| `owner@seed.test`     | Olga Owner       | Owner of seed-acme, the organization with enough members to page through                                    |
| `lastowner@seed.test` | Lars Lastowner   | The only owner of seed-solo: leaving and demotion are both refused for them                                 |
| `admin@seed.test`     | Ada Adminowa     | Administrator of seed-acme: manages members and roles, cannot delete the organization                       |
| `inviter@seed.test`   | Iwo Inviter      | Holds members.invite only, through a custom role: may send and withdraw invitations, may not remove anybody |
| `remover@seed.test`   | Rita Remover     | Holds members.remove only, through a custom role: the other side of the A6 split                            |
| `member@seed.test`    | Marek Member     | Plain member of seed-acme                                                                                   |
| `viewer@seed.test`    | Wiktor Viewer    | Read-only in seed-acme                                                                                      |
| `suspended@seed.test` | Zofia Suspended  | Suspended in seed-acme: still a member, every permission withdrawn                                          |
| `twofactor@seed.test` | Tomasz Twofactor | Two-factor enabled: signing in returns 202 and emails a code                                                |
| `multiorg@seed.test`  | Maja Multiorg    | Member of seed-acme, seed-globex and seed-solo, with a different role in each                               |
| `invited@seed.test`   | Ignacy Invited   | Has an account and a pending invitation to seed-globex, not yet accepted                                    |
| `nowhere@seed.test`   | Nina Nowhere     | Belongs to no organization: left everything, which is what a leaver looks like afterwards                   |
| `changing@seed.test`  | Cezary Changing  | Has a pending email change: the code was issued and never confirmed                                         |

Plus `abandonedowner@seed.test`, którego **nie da się użyć**: istnieje tylko po to, by zostać usuniętym. To on zostawia
`seed-abandoned` bez właściciela.

## Organizacje

| Slug             | Kształt                                | Po co                                                              |
|------------------|----------------------------------------|--------------------------------------------------------------------|
| `seed-acme`      | >100 członków, role własne, zawieszeni | paginacja i jej porządek, `(name, id)` przy remisach nazw          |
| `seed-globex`    | mała, z zaproszeniami                  | cykl życia oferty: oczekująca, do nieznanego adresu, wygasła       |
| `seed-solo`      | dokładnie jeden właściciel             | odmowa wyjścia i degradacji ostatniego właściciela                 |
| `seed-empty`     | pusta                                  | to, co produkuje endpoint platformowy przed wskazaniem właściciela |
| `seed-abandoned` | członkostwo-widmo, zero właścicieli    | `?without_owner=true`; **stan nieosiągalny przez API**             |

W `seed-acme` są trzy osoby o nazwisku **Jan Kowalski**. To nie żart: sortowanie po samej nazwie zostawia remisy, a
granica strony wewnątrz remisu jest miejscem, w którym wiersze giną.

Role własne `inviter` i `remover` rozdzielają `members.invite` od `members.remove` — czego **żadna rola shipowana nie
robi**, a co jest dokładnie rozstrzygnięciem A6.

## Tokeny i kody

Token zaproszenia istnieje **raz**, w wiadomości. Seeder jest jedynym miejscem, które go widzi, więc **wypisuje go w
logu** — bez tego nie da się przyjąć zaproszenia ręcznie. To samo dotyczy kodu oczekującej zmiany adresu.

## Determinizm

Losowa połowa danych idzie z ziarna `-seed` (domyślnie stałego), więc dwa biegi dają **te same sto osób**: błąd
znaleziony klikaniem jest odtwarzalny, a zrzuty ekranu nie przestają pasować do dokumentacji. Identyfikatory to uuid v7
i różnią się między bazami — dlatego ten dokument nazywa **adresy i slugi**, nigdy id.

## Idempotencja i reset

`task seed` można uruchamiać wielokrotnie: sprawdzone, że trzeci bieg zostawia **identyczny stan**, łącznie z wierszami
usuniętymi. Dlatego dopisanie nowej części i ponowny bieg nie wymaga czyszczenia.

`task seed:reset` usuwa dane seedera i sieje od nowa. Działa dzięki M9: unikaty na `users.email` i `organizations.slug`
są **częściowe** i obejmują tylko żywe wiersze, więc adresy i slugi wracają do użycia po miękkim usunięciu.

**Reset wycofuje dane, nie szoruje tabel.** Wiersze `memberships` po usuniętych kontach i organizacjach zostają —
nieosiągalne przez żadne zapytanie w systemie, bo każde filtruje usunięte. Twardego usuwania nie ma w domenie **celowo**
(pozycja znana i zapisana), a dorabianie go dla narzędzia rozwojowego byłoby dokładaniem właśnie tej zdolności, przed
którą chronią bezpieczniki. Jeśli chcesz naprawdę pustą bazę, użyj `task compose:reset`.

## Jak dopisać kolejny element

Seeder to **lista części** wykonywanych w kolejności:

```go
type Part interface {
	Name() string                              // dopasowywane przez -only i -skip
	Run(ctx context.Context, w *World) error
}
```

1. Nowy plik w [`internal/seed/`](../../internal/seed) z typem spełniającym `Part`.
2. Jedna linia w `Plan()` — **kolejność to kolejność zależności**, nie preferencja.
3. Czytaj to, co zrobiły wcześniejsze części, przez `World`: `w.castAccount(handle)`, `w.ensureOrganization(...)`,
   `w.role(ctx, orgID, key)`.

Czego **nie** trzeba pisać samodzielnie:

- **idempotencji** — mieszka w helperach `ensureAccount` / `ensureOrganization` / `ensureMember`, więc nowa część
  dostaje ją bez myślenia o tym,
- **aktora audytu** — `w.actingAs(ctx, userID)`; bez niego store nie zapisuje wiersza i zmiana jest niewidoczna w
  historii,
- **grantu** — `w.ownerGrant(orgID, userID)` zwraca to, co middleware rozstrzygnęłoby dla właściciela, z katalogu ról.
  Dzięki temu można wołać metody serwisu wymagające grantu i **reguły domenowe nadal się wykonują**: seeder nie może
  wyprodukować stanu, którego aplikacja by odmówiła.

Seeder jeździ po **serwisach domenowych**, nie po SQL-u — tak jak `cmd/bootstrap`. Dwa świadome wyjątki są oznaczone w
kodzie: `AddMember` i `CreateRole` to ścieżka provisioningu, repozytoryjna z tego samego powodu, z którego bootstrap
taki jest.

## Dlaczego `internal/seed`, a nie tylko `cmd/seed`

Bo cały plan wykonuje się w teście, na atrapach, w sekundę ([
`internal/seed/seed_test.go`](../../internal/seed/seed_test.go)). Seeder, którego nikt nie uruchamia w CI, psuje się po
cichu, a moment, w którym się o tym dowiadujesz, to próba odtworzenia czyjegoś błędu.

Ten test od razu na siebie zapracował: wykrył, że atrapa `memory.Authz` **nie widziała konta usuniętego** przez
`memory.Users` — a pakiet kontraktowy tego nie łapał, bo jego własny fixture mówił obu atrapom osobno. Seeder ma tylko
prawdziwe interfejsy, więc nie miał czym tego obejść.
