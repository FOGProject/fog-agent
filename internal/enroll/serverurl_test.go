package enroll

import "testing"

func TestHTTPSAddressMovesOnlyPlainHTTPOnTheDefaultPort(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		moved    bool
	}{
		{"http://fog.lab/fog", "https://fog.lab/fog", true},
		{"HTTP://fog.lab/fog/", "https://fog.lab/fog/", true},
		{"http://fog.lab:80/fog", "https://fog.lab/fog", true},
		{"http://10.0.0.5/fog", "https://10.0.0.5/fog", true},
		{"http://[fd00::5]/fog", "https://[fd00::5]/fog", true},
		{"http://[fd00::5]:80/fog", "https://[fd00::5]/fog", true},
		{"https://fog.lab/fog", "https://fog.lab/fog", false},
		{"http://fog.lab:8080/fog", "http://fog.lab:8080/fog", false},
		{"ftp://fog.lab/fog", "ftp://fog.lab/fog", false},
		{"fog.lab/fog", "fog.lab/fog", false},
		{"", "", false},
	} {
		got, moved := HTTPSAddress(tc.in)
		if got != tc.want || moved != tc.moved {
			t.Errorf("HTTPSAddress(%q) = %q, %t; want %q, %t", tc.in, got, moved, tc.want, tc.moved)
		}
	}
}
