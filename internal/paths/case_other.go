//go:build !darwin

package paths

// CaseSensitive reports that every volume is case-sensitive: off darwin, upool does not ask.
func CaseSensitive(string) (bool, error) {
	return true, nil
}
