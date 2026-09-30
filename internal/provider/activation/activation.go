// Package activation is the product-key activation provider (design 0016):
// the Windows product key converges on the one the host record carries,
// and Windows is asked to activate with it.
package activation

import (
	"context"
	"fmt"
	"strings"

	"github.com/FOGProject/fog-agent/internal/provider"
	"github.com/FOGProject/fog-agent/internal/secret"
)

// Desired is the activation block of the server's desired state.
type Desired struct {
	// Key is the hyphenated 29-character product key. It redacts itself
	// under every printer and marshaler, like the directory password, so
	// `--once` output and the state directory never carry it.
	Key secret.Secret `json:"key"`
}

// License is what Windows reports for its own product: the last five
// characters of the installed key ("" when none is installed) and whether
// it is licensed.
type License struct {
	Partial  string
	Licensed bool
}

// query, install and activate are the OS-specific halves, replaced in
// tests. Only install ever sees the key.
var (
	query    = osQuery
	install  = osInstall
	activate = osActivate
)

// Ensure installs d's key unless Windows already holds it, and activates
// unless Windows is already licensed. The detail names the key only by its
// last group, which is what Windows itself shows as the partial key.
func Ensure(ctx context.Context, d Desired) provider.Result {
	key := strings.ToUpper(strings.TrimSpace(d.Key.Reveal()))
	if len(key) != 29 {
		return provider.Result{Status: provider.StatusFailed, Detail: "desired key is not a 29-character product key"}
	}
	tail := key[24:]
	have, err := query(ctx)
	if err != nil {
		return provider.Result{Status: provider.StatusFailed, Detail: "reading the license state: " + err.Error()}
	}
	// Equality, not the legacy client's EndsWith: its EndsWith("") reported
	// a machine with no key installed as already done.
	if strings.EqualFold(have.Partial, tail) {
		if have.Licensed {
			return provider.Result{Status: provider.StatusUnchanged, Detail: "licensed with the key ending " + tail}
		}
		return activated(ctx, "key ending "+tail+" already installed")
	}
	if err := install(ctx, key); err != nil {
		return provider.Result{Status: provider.StatusFailed, Detail: fmt.Sprintf("installing the key ending %s: %v", tail, err)}
	}
	return activated(ctx, "installed the key ending "+tail)
}

// activated asks Windows to activate. A refusal here is still applied: the
// key is in place, and Windows retries activation on its own schedule.
// Reporting it failed would reinstall the same key on every poll, which is
// the legacy client's retry loop (design 0016 §4).
func activated(ctx context.Context, did string) provider.Result {
	if err := activate(ctx); err != nil {
		return provider.Result{Status: provider.StatusApplied, Detail: fmt.Sprintf("%s; activation pending: %v", did, err)}
	}
	return provider.Result{Status: provider.StatusApplied, Detail: did + "; activated"}
}
