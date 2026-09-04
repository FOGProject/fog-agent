package software

// registryDate turns the registry's InstallDate form, an 8-digit YYYYMMDD
// string with no separators, into YYYY-MM-DD. Anything that is not exactly 8
// digits is not a date the registry actually wrote, so it becomes "" rather
// than a guess.
//
// Kept in its own untagged file, separate from list_windows.go, so it can be
// unit-tested on any host without a registry or a windows build tag.
func registryDate(s string) string {
	if len(s) != 8 {
		return ""
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return s[0:4] + "-" + s[4:6] + "-" + s[6:8]
}
