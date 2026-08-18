package membership

// GrantsPermissions is the rule that only an active membership confers anything.
//
// It lives on the type rather than as an `== StatusActive` comparison at each
// call site, because those comparisons are what drift: one of them gets written
// as `!= StatusSuspended` and the next status added to the enum quietly grants
// everything.
func (s Status) GrantsPermissions() bool {
	return s == StatusActive
}
