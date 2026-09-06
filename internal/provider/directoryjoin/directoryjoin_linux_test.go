package directoryjoin

// The join itself is adcli or realm, tools that exist only on Linux
// (join_linux.go), and so are the symbols these tests reach for: loginUser
// and lookPath. They lived in directoryjoin_test.go, where
// `GOOS=windows go vet ./...` could not compile the package at all.

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestLoginUserStripsTheQualifier(t *testing.T) {
	// The server sends the form Windows takes and FOG has always stored;
	// adcli and realm treat `CORP\admin` as a user of that literal name.
	for in, want := range map[string]string{
		`CORP\fogjoin`:         "fogjoin",
		"fogjoin@corp.example": "fogjoin",
		"fogjoin":              "fogjoin",
		`  CORP\fogjoin  `:     "fogjoin",
		`CORP\sub\fogjoin`:     "fogjoin",
	} {
		if got := loginUser(in); got != want {
			t.Errorf("loginUser(%q) = %q, want %q", in, got, want)
		}
	}
}

// The requirement that picked these two tools: a command line is visible to
// every process on the machine, so the password may never be an argument.
func TestJoinCommandNeverPutsTheCredentialInAnArgument(t *testing.T) {
	restore := lookPath
	defer func() { lookPath = restore }()

	for _, tool := range []string{"adcli", "realm"} {
		lookPath = func(name string) (string, error) {
			if name == tool {
				return "/usr/sbin/" + tool, nil
			}
			return "", errors.New("not found")
		}
		name, args := joinCommand(policy())
		if name != tool {
			t.Fatalf("with only %s installed, picked %q", tool, name)
		}
		for _, a := range args {
			if strings.Contains(a, pw) {
				t.Fatalf("%s: the credential is in argv: %q", tool, a)
			}
		}
		joinedArgs := strings.Join(args, " ")
		if !strings.Contains(joinedArgs, "corp.example.com") {
			t.Errorf("%s: the domain is missing: %v", tool, args)
		}
		if !strings.Contains(joinedArgs, "OU=Workstations") {
			t.Errorf("%s: the OU is missing, so the object lands in CN=Computers: %v", tool, args)
		}
		if !strings.Contains(joinedArgs, "fogjoin") || strings.Contains(joinedArgs, `CORP\fogjoin`) {
			t.Errorf("%s: the account is not the bare name: %v", tool, args)
		}
	}
}

func TestJoinCommandOmitsAnEmptyOU(t *testing.T) {
	restore := lookPath
	defer func() { lookPath = restore }()
	lookPath = func(name string) (string, error) {
		if name == "adcli" {
			return "/usr/sbin/adcli", nil
		}
		return "", errors.New("not found")
	}
	p := policy()
	p.OU = ""
	_, args := joinCommand(p)
	for _, a := range args {
		// An empty --domain-ou= is not the same as omitting it: adcli
		// takes it as a request to create the object in an OU named "".
		if strings.HasPrefix(a, "--domain-ou") {
			t.Fatalf("sent an empty OU: %q", a)
		}
	}
}

func TestLinuxJoinFeedsThePasswordOnStdin(t *testing.T) {
	restoreLook, restoreRun := lookPath, runJoin
	defer func() { lookPath, runJoin = restoreLook, restoreRun }()
	lookPath = func(name string) (string, error) {
		if name == "adcli" {
			return "/usr/sbin/adcli", nil
		}
		return "", errors.New("not found")
	}
	var sawStdin string
	runJoin = func(_ context.Context, password, _ string, _ ...string) (string, error) {
		sawStdin = password
		return "", nil
	}
	if r := (Linux{}).Join(context.Background(), policy()); r.Status != StatusJoined {
		t.Fatalf("got %+v", r)
	}
	if sawStdin != pw {
		t.Fatalf("stdin carried %q, not the credential", sawStdin)
	}
}

// A Linux machine is a domain member as soon as the join returns; only
// Windows has to restart. Asking for a reboot that is not needed is a
// gratuitous outage on somebody's workstation.
func TestLinuxJoinDoesNotAskForAReboot(t *testing.T) {
	restoreLook, restoreRun := lookPath, runJoin
	defer func() { lookPath, runJoin = restoreLook, restoreRun }()
	lookPath = func(string) (string, error) { return "/usr/sbin/adcli", nil }
	runJoin = func(context.Context, string, string, ...string) (string, error) { return "", nil }
	if r := (Linux{}).Join(context.Background(), policy()); r.Reboot {
		t.Fatal("asked for a reboot Linux does not need")
	}
}

func TestLinuxJoinReportsTheToolsOwnWords(t *testing.T) {
	restoreLook, restoreRun := lookPath, runJoin
	defer func() { lookPath, runJoin = restoreLook, restoreRun }()
	lookPath = func(string) (string, error) { return "/usr/sbin/adcli", nil }
	runJoin = func(context.Context, string, string, ...string) (string, error) {
		return "* Using domain name: corp.example.com\n" +
			"adcli: Insufficient access\n", exec.ErrNotFound
	}
	r := (Linux{}).Join(context.Background(), policy())
	if r.Status != StatusFailed {
		t.Fatalf("status = %q", r.Status)
	}
	// The LAST line: adcli narrates its progress and the failure is at the
	// end, which is the opposite of lpadmin's shape.
	if r.Error != "adcli: Insufficient access" {
		t.Errorf("error = %q", r.Error)
	}
}

func TestLinuxSaysWhenNeitherToolIsInstalled(t *testing.T) {
	restore := lookPath
	defer func() { lookPath = restore }()
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	ok, why := (Linux{}).Available()
	if ok {
		t.Fatal("claimed a join was possible with no tooling")
	}
	if !strings.Contains(why, "adcli") || !strings.Contains(why, "realm") {
		t.Errorf("why = %q -- name both, so an admin knows what to install", why)
	}
}
