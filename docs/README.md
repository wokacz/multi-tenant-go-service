# Dokumentacja

Opis autoryzacji i mechanizmów, które ją otaczają. Kod jest źródłem prawdy —
te strony tłumaczą **dlaczego** rzeczy wyglądają tak, jak wyglądają.

## Model w skrócie

Konto jest chronione **hasłem**. Sesja to **token JWT**, który wskazuje
użytkownika *i* urządzenie. Konto może dodatkowo wymagać **kodu z maila** przy
logowaniu z urządzenia, któremu jeszcze nie zaufano.

```
hasło  ──▶  urządzenie  ──▶  [ kod z maila ]  ──▶  token
```

## Spis treści

| Strona | O czym |
| --- | --- |
| [Przegląd](przeglad.md) | Pełny obraz: warstwy, przepływ logowania, gdzie zapadają decyzje |
| [Tokeny i sesje](tokeny-i-sesje.md) | JWT, epoka sesji, unieważnianie, domyślne „deny" |
| [Urządzenia](urzadzenia.md) | Rozpoznawanie klienta, zaufanie, odwoływanie, historia logowań |
| [Drugi składnik](dwuskladnikowe.md) | 2FA mailem i reset hasła — wspólna mechanika kodów |
| [Ochrona](ochrona.md) | Limity, odporność na enumerację kont, koszt hashowania, nagłówki |

## Skąd to czytać dalej

- Kontrakt HTTP: [`api/openapi.yaml`](../api/openapi.yaml) (generowany, commitowany)
- Zasady dla pracy nad kodem: [`CLAUDE.md`](../CLAUDE.md)
- Uruchomienie lokalne: [`README.md`](../README.md)
