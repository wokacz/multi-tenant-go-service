# Nowe uprawnienie

Uprawnienie istnieje dokładnie wtedy, gdy jakiś handler go pilnuje. Dodanie go „na zapas" tworzy pozycję, którą
administrator zobaczy w edytorze ról, wstawi do roli i będzie się zastanawiał, dlaczego nic nie robi.

`TestEveryPermissionGuardsAnOperation` przewraca build za taki wpis. Nie ma listy wyjątków — wyjątek zamienia
„zdefiniuję teraz, podepnę później" w decyzję jednolinijkową, a „później" jest dokładnie tym momentem, o którym się
zapomina.

## Lista kontrolna

1. Stała i wpis w katalogu — `internal/domain/authz/permission.go`
2. Rola, która je nadaje — `internal/domain/authz/role.go`
3. Tłumaczenia we **wszystkich** językach — `internal/i18n/locales/*.json`
4. Reguła operacji — `internal/api/authz.go`
5. Endpoint, który je egzekwuje
6. `task check`

## 1. Katalog

```go
const (
	...
	PermWidgetsRead   Permission = "widgets.read"
	PermWidgetsUpdate Permission = "widgets.update"
)

var catalog = []Definition{
	...
	{PermWidgetsRead, ScopeOrganization, "widgets"},
	{PermWidgetsUpdate, ScopeOrganization, "widgets"},
}
```

Nazewnictwo: `[platform.]<zasób>[.<podzasób>].<akcja>`. Akcja pochodzi z zamkniętej listy `validActions` i jest **zawsze
ostatnia**. Nowy moduł to nowy prefiks zasobu, nigdy głębsze zagnieżdżenie w istniejącym.

`Group` decyduje o nagłówku w ekranie ustawień i jest wypisany wprost, żeby dało się przeorganizować ekran bez zmiany
klucza — a zmiana klucza to zmiana kontraktu.

## 2. Role

`owner` i `platform_admin` są **wyprowadzane** z katalogu (`InScope(...)`), więc nowe uprawnienie trafia do nich
automatycznie. To celowe: uprawnienie, którego nie ma żadna rola, jest funkcją niedziałającą dla nikogo i niezgłaszającą
powodu.

`admin`, `member` i `viewer` mają **wypisane** listy. Też celowo: nowe uprawnienie lądujące automatycznie u każdego
administratora to cicha zmiana przywilejów dowieziona razem z niepowiązaną funkcją. Dopisz je, jeśli tam należy:

```go
{
	Key:   RoleAdmin,
	Permissions: []Permission{
		...
		PermWidgetsRead,
		PermWidgetsUpdate,
	},
},
```

## 3. Tłumaczenia

W **każdym** pliku `internal/i18n/locales/*.json`:

```json
"permission.widgets.read.name": "Podgląd widgetów",
"permission.widgets.read.description": "Wgląd w widgety organizacji."
```

Brak choćby jednego klucza w jednym języku przewraca
`TestEveryPermissionIsTranslatedInEveryLocale`. Nazwa i opis trafiają do edytora ról, więc opis powinien mówić, **co ta
osoba będzie mogła zrobić**, nie powtarzać nazwy.

## 4. Reguła operacji

```go
var operationAccess = map[string]accessRule{
	...
	"list-widgets":  {authz.PermWidgetsRead, authz.ScopeOrganization},
	"update-widget": {authz.PermWidgetsUpdate, authz.ScopeOrganization},
}
```

Zakres musi pasować do ścieżki: `ScopeOrganization` wymaga `{orgID}`,
`ScopeSystem` wymaga jego braku.

## 5. Endpoint

[Instrukcja nowego endpointu](002_add_endpoint.md). Operacja musi deklarować
`403` (i `404` przy zakresie organizacyjnym) w `Errors`.

## 6. Sprawdzenie

```bash
task check
```

Testy, które wyłapią niekompletną robotę:

| Test                                           | Czego pilnuje                               |
|------------------------------------------------|---------------------------------------------|
| `TestEveryPermissionGuardsAnOperation`         | uprawnienie bez operacji                    |
| `TestOwnerCoversEveryOrganizationPermission`   | uprawnienie, którego nie ma żadna rola      |
| `TestAccessRulesNamePermissionsThatExist`      | reguła wskazująca nieistniejące uprawnienie |
| `TestOrgScopedRulesLiveOnOrgScopedPaths`       | zakres niezgodny ze ścieżką                 |
| `TestGatedOperationsDeclareTheirRefusals`      | brak 403/404 w `Errors`                     |
| `TestEveryPermissionIsTranslatedInEveryLocale` | brakujące tłumaczenie                       |
| `TestTheSnapshotAgreesWithEnforcement`         | migawka niezgodna z egzekwowaniem           |

## Usunięcie uprawnienia

Usuń stałą, wpis w katalogu, tłumaczenia i regułę. **Wierszy w
`role_permissions` nie trzeba czyścić**: `authz.Sanitize` ignoruje klucze spoza katalogu, więc nieaktualny wiersz
przestaje cokolwiek nadawać z chwilą wdrożenia.

Endpointy ról raportują takie klucze **bez filtrowania** — ekran ustawień musi je zobaczyć, żeby administrator mógł je
usunąć z roli.
