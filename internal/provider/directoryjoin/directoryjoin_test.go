package directoryjoin

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/FOGProject/fog-agent/internal/directory"
	"github.com/FOGProject/fog-agent/internal/secret"
)

const pw = "hunter2-correct-horse"

func policy() Policy {
	return Policy{
		Domain: "corp.example.com", Netbios: "CORP",
		OU:       "OU=Workstations,DC=corp,DC=example,DC=com",
		Username: `CORP\fogjoin`, Password: secret.New(pw), Reboot: true,
	}
}

func joined(domain, netbios string) directory.Directory {
	return directory.Directory{Joined: true, Kind: directory.KindAD,
		Domain: domain, Netbios: netbios}
}

// ptr is here because Converge takes the policy by pointer, so the zeroing
// reaches the caller's copy.
func ptr(p Policy) *Policy { return &p }

func unjoined() directory.Directory {
	return directory.Directory{Kind: directory.KindWorkgroup}
}

func TestDecideJoinsAnUnjoinedMachine(t *testing.T) {
	if got, why := Decide(policy(), unjoined()); got != Join {
		t.Fatalf("got %s (%s), want join", got, why)
	}
}

func TestDecideLeavesAJoinedMachineAlone(t *testing.T) {
	got, why := Decide(policy(), joined("corp.example.com", "CORP"))
	if got != None {
		t.Fatalf("got %s, want none", got)
	}
	if !strings.Contains(why, "already in") {
		t.Errorf("why = %q", why)
	}
}

// The failure that would matter most: reading a correctly joined machine as
// unjoined runs a join against a live domain controller every poll, forever.
// FOG's hostADDomain holds whichever form the admin typed, so both count.
func TestDecideRecognizesTheDomainInEitherForm(t *testing.T) {
	cases := []struct {
		name     string
		observed directory.Directory
		policy   Policy
	}{
		{"dns both sides", joined("corp.example.com", "CORP"), policy()},
		{"machine reports only the short name",
			directory.Directory{Joined: true, Netbios: "CORP"}, policy()},
		{"policy names the short form",
			joined("corp.example.com", "CORP"),
			Policy{Domain: "CORP", Username: "u", Password: secret.New(pw)}},
		// The server does not always know the short name -- nothing on the
		// host record holds it, so it is only there when a previous facts
		// report supplied it. A machine that reports ONLY the short name
		// against a policy that carries only the dotted one has to match on
		// the first label, or a correctly joined machine is re-joined every
		// poll for want of a field neither side is required to have.
		{"neither side has a netbios to compare",
			directory.Directory{Joined: true, Netbios: "CORP"},
			Policy{Domain: "corp.example.com", Username: "u", Password: secret.New(pw)}},
		{"case differs", joined("CORP.EXAMPLE.COM", "corp"), policy()},
	}
	for _, c := range cases {
		if got, why := Decide(c.policy, c.observed); got != None {
			t.Errorf("%s: got %s (%s), want none", c.name, got, why)
		}
	}
}

// An empty netbios on both sides is not a match: two machines that know
// nothing about their short name are not thereby in the same domain.
func TestDecideDoesNotMatchTwoEmptyShortNames(t *testing.T) {
	observed := directory.Directory{Joined: true, Domain: "other.example.com"}
	p := Policy{Domain: "corp.example.com", Username: "u", Password: secret.New(pw)}
	if got, _ := Decide(p, observed); got == None {
		t.Fatal("two empty netbios names matched each other")
	}
}

// The refusal that keeps the destructive act out of this package: getting a
// machine from one domain to another means LEAVING the first, which resets
// the computer account's password and can cost it its SID, its BitLocker
// escrow and its certificates.
func TestDecideRefusesToMoveBetweenDomains(t *testing.T) {
	got, why := Decide(policy(), joined("old.example.com", "OLD"))
	if got != Refuse {
		t.Fatalf("got %s, want refuse -- this must never become an unjoin", got)
	}
	if !strings.Contains(why, "old.example.com") || !strings.Contains(why, "corp.example.com") {
		t.Errorf("why = %q; the admin needs both domains named", why)
	}
}

// An unauthenticated join attempt is a failed authentication against
// somebody's domain controller, repeated once a poll -- which is how a
// service account gets locked out.
func TestDecideRefusesWithoutACredential(t *testing.T) {
	for _, c := range []struct {
		name string
		p    Policy
	}{
		{"no password", Policy{Domain: "corp.example.com", Username: "u"}},
		{"blank password", Policy{Domain: "corp.example.com", Username: "u", Password: secret.New("  ")}},
		{"no username", Policy{Domain: "corp.example.com", Password: secret.New(pw)}},
	} {
		got, why := Decide(c.p, unjoined())
		if got != Refuse {
			t.Errorf("%s: got %s, want refuse", c.name, got)
		}
		if !strings.Contains(why, "credential") {
			t.Errorf("%s: why = %q", c.name, why)
		}
	}
}

func TestDecideRefusesWithNoDomain(t *testing.T) {
	got, why := Decide(Policy{Username: "u", Password: secret.New(pw)}, unjoined())
	if got != Refuse || !strings.Contains(why, "no domain") {
		t.Fatalf("got %s (%s)", got, why)
	}
}

type fakeBackend struct {
	calls     int
	available bool
	missing   string
	result    Result
	sawPass   string
}

func (f *fakeBackend) Available() (bool, string) { return f.available, f.missing }
func (f *fakeBackend) Join(_ context.Context, p Policy) Result {
	f.calls++
	f.sawPass = p.Password.Reveal()
	return f.result
}

func TestConvergeJoinsAndAsksForTheReboot(t *testing.T) {
	b := &fakeBackend{available: true, result: Result{Status: StatusJoined, Reboot: true}}
	r := Converge(context.Background(), b, ptr(policy()), unjoined())
	if r.Status != StatusJoined || !r.Reboot {
		t.Fatalf("got %+v", r)
	}
	if b.sawPass != pw {
		t.Errorf("the backend was handed %q, not the credential", b.sawPass)
	}
}

// The host's "Enforce Hostname | AD Join Reboots" flag is a permission, and
// a platform needing a reboot does not override an admin saying no.
func TestConvergeDoesNotAskForARebootTheHostForbids(t *testing.T) {
	p := policy()
	p.Reboot = false
	b := &fakeBackend{available: true, result: Result{Status: StatusJoined, Reboot: true}}
	if r := Converge(context.Background(), b, &p, unjoined()); r.Reboot {
		t.Fatal("rebooted against the host's policy")
	}
}

// The server reads already_joined as "still true" and silence as unknown.
func TestConvergeReportsTheRestingStateWithoutTouchingTheBackend(t *testing.T) {
	b := &fakeBackend{available: true}
	r := Converge(context.Background(), b, ptr(policy()), joined("corp.example.com", "CORP"))
	if r.Status != StatusAlreadyJoined {
		t.Fatalf("status = %q", r.Status)
	}
	if b.calls != 0 {
		t.Fatal("contacted the domain controller for a machine already in the domain")
	}
}

func TestConvergeRefusesBeforeItLooksForTooling(t *testing.T) {
	// A refusal is about the policy, not the machine: it must not be
	// reported as "unsupported" on a host that happens to lack adcli.
	b := &fakeBackend{available: false, missing: "no adcli"}
	r := Converge(context.Background(), b, ptr(policy()), joined("old.example.com", "OLD"))
	if r.Status != StatusRefused {
		t.Fatalf("status = %q, want refused", r.Status)
	}
	if b.calls != 0 {
		t.Fatal("attempted a join it had refused")
	}
}

func TestConvergeReportsMissingTooling(t *testing.T) {
	b := &fakeBackend{available: false, missing: "neither adcli nor realm is installed"}
	r := Converge(context.Background(), b, ptr(policy()), unjoined())
	if r.Status != StatusUnsupported {
		t.Fatalf("status = %q", r.Status)
	}
	if !strings.Contains(r.Error, "adcli") {
		t.Errorf("error = %q -- this is the line the admin reads", r.Error)
	}
	if b.calls != 0 {
		t.Fatal("joined with no join tool")
	}
}

func TestConvergeTruncatesAToolsNovel(t *testing.T) {
	b := &fakeBackend{available: true,
		result: Result{Status: StatusFailed, Error: strings.Repeat("x", 4000)}}
	r := Converge(context.Background(), b, ptr(policy()), unjoined())
	if len(r.Error) != MaxError {
		t.Fatalf("kept %d bytes; the server column holds %d", len(r.Error), MaxError)
	}
}

// Design 0009 §6: held in memory only, zeroed after the attempt. The caller
// still holds the struct, and it must not be able to print or persist the
// value once Converge has returned.
func TestConvergeZeroesTheCredential(t *testing.T) {
	p := policy()
	Converge(context.Background(), &fakeBackend{available: true,
		result: Result{Status: StatusJoined}}, &p, unjoined())
	if !p.Password.Empty() {
		t.Fatalf("the credential survived the attempt: %q", p.Password.Reveal())
	}
}

// Nothing in a Report may carry the credential: Detail and Error both end up
// in the agent's log and in an audit row on the server.
func TestNoReportFieldCarriesTheCredential(t *testing.T) {
	for _, observed := range []directory.Directory{
		unjoined(), joined("corp.example.com", "CORP"), joined("old.example.com", "OLD"),
	} {
		b := &fakeBackend{available: true,
			result: Result{Status: StatusFailed, Error: "adcli: Insufficient access"}}
		r := Converge(context.Background(), b, ptr(policy()), observed)
		for _, field := range []string{r.Status, r.Error, r.Detail} {
			if strings.Contains(field, pw) {
				t.Fatalf("a report field carried the credential: %q", field)
			}
		}
	}
}

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
