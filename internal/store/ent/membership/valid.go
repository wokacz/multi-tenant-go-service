package membership

// Valid is StatusValidator as a method, so callers can ask the type instead of
// importing the generated validator by name.
func (s Status) Valid() bool {
	return StatusValidator(s) == nil
}
