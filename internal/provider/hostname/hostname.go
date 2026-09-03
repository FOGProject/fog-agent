// Package hostname is the `ensure hostname` provider (design 0001 section
// 7): the machine's name converges on the name its host record carries.
package hostname

import (
	"fmt"
	"strings"

	"github.com/FOGProject/fog-agent/internal/provider"
)

// Desired is the hostname block of the server's desired state.
type Desired struct {
	Name string `json:"name"`
	// Enforce is the host's "Enforce Hostname | AD Join Reboots" flag: the
	// admin's permission to reboot to finish a rename. Nothing here
	// reboots; a rename that needs one is reported as pending_reboot and
	// the reboot coordinator, when it exists, will consult this.
	Enforce bool `json:"enforce"`
}

// current and set are the OS-specific halves, replaced in tests.
var (
	current = osCurrent
	set     = osSet
)

// Ensure reconciles the machine's name with d and reports. Names compare
// case-insensitively: DNS and NetBIOS both do, and a rename that only
// changes case is not a rename anyone asked for.
func Ensure(d Desired) provider.Result {
	want := strings.TrimSpace(d.Name)
	if want == "" {
		return provider.Result{Status: provider.StatusFailed, Detail: "desired name is empty"}
	}
	have, err := current()
	if err != nil {
		return provider.Result{Status: provider.StatusFailed, Detail: "reading the hostname: " + err.Error()}
	}
	if strings.EqualFold(have, want) {
		return provider.Result{Status: provider.StatusUnchanged, Detail: have}
	}
	reboot, err := set(want)
	if err != nil {
		return provider.Result{Status: provider.StatusFailed, Detail: fmt.Sprintf("%s -> %s: %v", have, want, err)}
	}
	detail := fmt.Sprintf("%s -> %s", have, want)
	if reboot {
		return provider.Result{Status: provider.StatusPendingReboot, Detail: detail}
	}
	return provider.Result{Status: provider.StatusApplied, Detail: detail}
}
