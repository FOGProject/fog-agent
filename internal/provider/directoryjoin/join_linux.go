package directoryjoin

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// Timeout bounds one join. Contacting a domain controller that is not there
// must not hold the poll open.
const Timeout = 3 * time.Minute

// Linux joins through adcli, or realm where adcli is not installed.
//
// Both take the password on STDIN, which is the requirement that picks them:
// a command line is visible to every process on the machine, so a join
// password may never be an argument. adcli is preferred because it says so
// explicitly (`--stdin-password`) and does one thing; realm is the
// higher-level tool that most distributions ship, and it drives adcli
// underneath, so a machine with realm and no adcli is joined the same way in
// the end.
type Linux struct{}

// Probes as vars so the tests can drive the decision without a domain.
var (
	lookPath = exec.LookPath
	runJoin  = func(ctx context.Context, password string, name string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		// The one channel that is not visible in the process table. A
		// trailing newline because both tools read a line.
		cmd.Stdin = strings.NewReader(password + "\n")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
)

// Available reports whether either join tool is installed.
func (Linux) Available() (bool, string) {
	if _, err := lookPath("adcli"); err == nil {
		return true, ""
	}
	if _, err := lookPath("realm"); err == nil {
		return true, ""
	}
	return false, "neither adcli nor realm is installed; this machine cannot be joined"
}

// Join adds the machine to the domain.
func (Linux) Join(ctx context.Context, p Policy) Result {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	name, args := joinCommand(p)
	if name == "" {
		return Result{Status: StatusUnsupported,
			Error: "neither adcli nor realm is installed; this machine cannot be joined"}
	}
	out, err := runJoin(ctx, p.Password.Reveal(), name, args...)
	if err != nil {
		return Result{Status: StatusFailed, Error: message(out, err)}
	}
	// No reboot: a Linux machine is a domain member as soon as the join
	// returns. Windows is the one that has to restart.
	return Result{Status: StatusJoined}
}

// joinCommand picks the tool and builds its arguments. Separated from the
// run so the argument shape is a test case -- and so it is provable that the
// password is not among them.
func joinCommand(p Policy) (string, []string) {
	if _, err := lookPath("adcli"); err == nil {
		args := []string{"join", "--domain=" + p.Domain,
			"--login-user=" + loginUser(p.Username), "--stdin-password"}
		if p.OU != "" {
			args = append(args, "--domain-ou="+p.OU)
		}
		return "adcli", args
	}
	if _, err := lookPath("realm"); err == nil {
		args := []string{"join", "--user=" + loginUser(p.Username)}
		if p.OU != "" {
			args = append(args, "--computer-ou="+p.OU)
		}
		return "realm", append(args, p.Domain)
	}
	return "", nil
}

// loginUser is the account name these tools want.
//
// The server sends the account domain-qualified, because that is the form
// Windows takes and the form FOG has always stored. adcli and realm want the
// bare sAMAccountName and treat `CORP\admin` as a user literally called
// `CORP\admin`, so the qualifier comes off here rather than the server
// sending two spellings of one account.
func loginUser(user string) string {
	user = strings.TrimSpace(user)
	if i := strings.LastIndex(user, `\`); i >= 0 {
		user = user[i+1:]
	}
	if i := strings.Index(user, "@"); i > 0 {
		user = user[:i]
	}
	return user
}

// message is the line an admin sees: the tool's own words when it had any,
// else the exit error.
func message(out string, err error) string {
	out = strings.TrimSpace(out)
	if out != "" {
		// The LAST line, not the first: adcli prints its progress as it
		// goes and the failure is the end of it, which is the opposite of
		// lpadmin.
		if i := strings.LastIndexByte(out, '\n'); i >= 0 && i < len(out)-1 {
			out = out[i+1:]
		}
		return out
	}
	if err != nil {
		return err.Error()
	}
	return ""
}
