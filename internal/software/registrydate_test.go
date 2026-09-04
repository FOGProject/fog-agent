package software

import "testing"

func TestRegistryDate(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"20240702", "2024-07-02"},
		{"", ""},
		{"2024-07-02", ""}, // already formatted is not the 8-digit form the registry writes
		{"notadate", ""},   // not digits at all
		{"202407021", ""},  // one digit too many
	}
	for _, c := range cases {
		if got := registryDate(c.in); got != c.want {
			t.Errorf("registryDate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
