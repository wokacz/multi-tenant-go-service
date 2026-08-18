// Package i18n resolves the language a response should be written in, and looks
// up the messages for it.
//
// The catalogs are compiled into the binary. That is deliberate for everything
// the code itself produces — error messages, permission names, the names of the
// roles that ship with the product — because those change with the code, go
// through review with it, and must not be missing at runtime. Text created by
// users, such as the name of a role somebody defined in the settings screen,
// belongs in the database instead; see models.RoleTranslation.
package i18n

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"golang.org/x/text/language"
)

// Locale is a BCP 47 tag.
type Locale string

// Fallback is the language used when nothing else matches. Every key must exist
// in it, which is what makes the fallback total rather than best-effort.
const Fallback Locale = "en"

//go:embed locales/*.json
var files embed.FS

// Catalog holds every message in every shipped language.
type Catalog struct {
	messages map[Locale]map[string]string
	locales  []Locale
	matcher  language.Matcher
}

// Default is the catalog built from the embedded files.
//
// Built once, lazily, and never mutated — so there is no Install to call, no
// injection to thread through, and no data race between servers built
// concurrently in tests.
var Default = sync.OnceValue(func() *Catalog {
	catalog, err := load()
	if err != nil {
		// The files are embedded, so a failure here means the binary was built
		// from a broken tree. There is no useful degraded mode: every error
		// response in the process would have no text.
		panic("i18n: loading embedded catalogs: " + err.Error())
	}

	return catalog
})

func load() (*Catalog, error) {
	entries, err := files.ReadDir("locales")
	if err != nil {
		return nil, err
	}

	catalog := &Catalog{messages: map[Locale]map[string]string{}}

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}

		raw, err := files.ReadFile("locales/" + name)
		if err != nil {
			return nil, err
		}

		var messages map[string]string
		if err := json.Unmarshal(raw, &messages); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}

		catalog.messages[Locale(strings.TrimSuffix(name, ".json"))] = messages
	}

	if _, ok := catalog.messages[Fallback]; !ok {
		return nil, fmt.Errorf("no catalog for the fallback language %q", Fallback)
	}

	// The fallback leads the matcher's tag list: golang.org/x/text treats the
	// first tag as the default, and it is what an unmatched Accept-Language
	// resolves to.
	catalog.locales = append(catalog.locales, Fallback)

	for locale := range catalog.messages {
		if locale != Fallback {
			catalog.locales = append(catalog.locales, locale)
		}
	}

	slices.Sort(catalog.locales[1:])

	tags := make([]language.Tag, 0, len(catalog.locales))
	for _, locale := range catalog.locales {
		tag, err := language.Parse(string(locale))
		if err != nil {
			return nil, fmt.Errorf("locale %q: %w", locale, err)
		}

		tags = append(tags, tag)
	}

	catalog.matcher = language.NewMatcher(tags)

	return catalog, nil
}

// Locales lists the shipped languages, fallback first.
func (c *Catalog) Locales() []Locale {
	return slices.Clone(c.locales)
}

// Keys lists every message key defined for a locale. Used by the completeness
// tests rather than at runtime.
func (c *Catalog) Keys(locale Locale) []string {
	out := make([]string, 0, len(c.messages[locale]))
	for key := range c.messages[locale] {
		out = append(out, key)
	}

	slices.Sort(out)

	return out
}

// Negotiate picks the language for a response.
//
// The account's stored preference wins over the request header: somebody who
// set their language in the product means it, and their browser's Accept-Language
// is often whatever the machine was installed with. The header decides for
// callers who have not chosen — which includes every anonymous request.
//
// Parsing is delegated to golang.org/x/text rather than split on commas: a hand
// rolled parser gets q-weights wrong, and does not know that pl-PL should match
// a catalog that only has pl.
func (c *Catalog) Negotiate(acceptLanguage, preference string) Locale {
	if preference != "" {
		if locale, ok := c.Resolve(preference); ok {
			return locale
		}
	}

	if acceptLanguage == "" {
		return Fallback
	}

	desired, _, err := language.ParseAcceptLanguage(acceptLanguage)
	if err != nil || len(desired) == 0 {
		return Fallback
	}

	_, index, confidence := c.matcher.Match(desired...)
	if confidence == language.No {
		return Fallback
	}

	return c.locales[index]
}

// Match reports which shipped language an Accept-Language header actually asked
// for, and whether it asked for one at all.
//
// It is not Negotiate. Negotiate always answers, falling back when nothing
// matches, because a response has to be written in something. Match reports the
// absence, which is what a caller deciding whether to *remember* a preference
// needs: storing the fallback for somebody who never expressed a choice turns a
// guess into a permanent decision, and their browser's language would then be
// ignored forever after.
func (c *Catalog) Match(acceptLanguage string) (Locale, bool) {
	if acceptLanguage == "" {
		return Fallback, false
	}

	desired, _, err := language.ParseAcceptLanguage(acceptLanguage)
	if err != nil || len(desired) == 0 {
		return Fallback, false
	}

	_, index, confidence := c.matcher.Match(desired...)
	if confidence == language.No {
		return Fallback, false
	}

	return c.locales[index], true
}

// Resolve maps one tag onto a shipped catalog, allowing pl-PL to find pl.
//
// It is the counterpart of Match for a stored preference rather than a header,
// and like Match it reports the absence instead of falling back: a caller
// *setting* a language needs to hear that there is no such catalog, where a
// caller *rendering* a response needs an answer whatever happens.
func (c *Catalog) Resolve(tag string) (Locale, bool) {
	if _, ok := c.messages[Locale(tag)]; ok {
		return Locale(tag), true
	}

	parsed, err := language.Parse(tag)
	if err != nil {
		return "", false
	}

	_, index, confidence := c.matcher.Match(parsed)
	if confidence == language.No {
		return "", false
	}

	return c.locales[index], true
}

// T looks up a message, formatting it with args when the message has verbs.
//
// The lookup falls back from the exact locale to its base language, then to the
// fallback catalog, and finally to the key itself. Returning the key rather than
// an empty string is deliberate: a missing message then shows up in the response
// as something greppable instead of as a blank field nobody notices. The
// completeness tests are what stop it happening at all.
func (c *Catalog) T(locale Locale, key string, args ...any) string {
	if message, ok := c.lookup(locale, key); ok {
		return format(message, args)
	}

	if base, _, ok := strings.Cut(string(locale), "-"); ok {
		if message, found := c.lookup(Locale(base), key); found {
			return format(message, args)
		}
	}

	if message, ok := c.lookup(Fallback, key); ok {
		return format(message, args)
	}

	return key
}

// Has reports whether the key is defined for the locale exactly, with no
// fallback. The completeness tests use it; nothing at runtime does.
func (c *Catalog) Has(locale Locale, key string) bool {
	_, ok := c.lookup(locale, key)

	return ok
}

func (c *Catalog) lookup(locale Locale, key string) (string, bool) {
	messages, ok := c.messages[locale]
	if !ok {
		return "", false
	}

	message, ok := messages[key]

	return message, ok
}

func format(message string, args []any) string {
	if len(args) == 0 {
		return message
	}

	return fmt.Sprintf(message, args...)
}

// ctxKey keeps the context key unexported so no other package can collide with
// it, which is the whole reason for the named type.
type ctxKey int

const localeKey ctxKey = iota

// WithLocale attaches the negotiated language to the context.
func WithLocale(ctx context.Context, locale Locale) context.Context {
	return context.WithValue(ctx, localeKey, locale)
}

// LocaleFrom returns the negotiated language, or the fallback.
//
// The fallback matters for anything running outside a request — a background
// job, a test — where losing the message entirely would be worse than losing
// the translation.
func LocaleFrom(ctx context.Context) Locale {
	if locale, ok := ctx.Value(localeKey).(Locale); ok && locale != "" {
		return locale
	}

	return Fallback
}
