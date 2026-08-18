package seed

// Deterministic fake data, generated from World.Rand rather than pulled from a
// dependency.
//
// A library would give richer names and one more module to keep current for a tool
// that only has to produce plausible strings. These lists are Polish and English on
// purpose: the product ships two languages, and a member list where every name is
// ASCII hides exactly the rendering problems worth finding.
var (
	firstNames = []string{
		"Agnieszka", "Bartosz", "Cecylia", "Dorota", "Emil", "Franciszek",
		"Grażyna", "Henryk", "Irena", "Jakub", "Katarzyna", "Łukasz",
		"Małgorzata", "Norbert", "Ola", "Paweł", "Rafał", "Sylwia",
		"Tomasz", "Urszula", "Wojciech", "Zuzanna", "Alice", "Bob",
		"Charlie", "Diana", "Edward", "Fiona", "George", "Hannah",
	}

	lastNames = []string{
		"Nowak", "Kowalczyk", "Wiśniewska", "Wójcik", "Kowalska", "Zielińska",
		"Szymański", "Woźniak", "Dąbrowski", "Kozłowska", "Jankowski", "Mazur",
		"Krawczyk", "Piotrowska", "Grabowski", "Nowicka", "Pawłowski",
		"Michalska", "Adamczyk", "Dudek", "Smith", "Jones", "Taylor", "Brown",
	}

	locales = []string{"pl", "en", ""}
)

// fakeName is a first and last name drawn from the lists.
func (w *World) fakeName() string {
	first := firstNames[w.Rand.IntN(len(firstNames))]
	last := lastNames[w.Rand.IntN(len(lastNames))]

	return first + " " + last
}

// fakeLocale includes the empty string, which is a real state: an account that
// never expressed a language preference negotiates one per request, and code that
// assumes the column is always populated breaks on exactly those accounts.
func (w *World) fakeLocale() string {
	return locales[w.Rand.IntN(len(locales))]
}
