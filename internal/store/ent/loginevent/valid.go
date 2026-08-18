package loginevent

// Valid is OutcomeValidator as a method, so callers can ask the type instead of
// importing the generated validator by name.
func (o Outcome) Valid() bool {
	return OutcomeValidator(o) == nil
}
