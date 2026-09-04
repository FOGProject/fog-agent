// Package software lists the programs actually installed on the host (design
// 0006). This is an observation, distinct from the desired-state install
// capability in internal/provider/software: that one holds the host to what
// an admin wants; this one reports what is there. The list rides the poll
// request when its hash changes and lands in the server's hostSoftware table.
package software

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
)

// Program is one installed program as the OS reports it.
type Program struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Publisher is the vendor where the OS records one, else empty.
	Publisher string `json:"publisher"`
	// Source is where the fact came from: registry, winget, dpkg, rpm,
	// flatpak, snap, pkgutil, brew. It is part of a program's identity on
	// the server, so the same name from two managers is two rows.
	Source string `json:"source"`
	Arch   string `json:"arch"`
	// InstallDate is YYYY-MM-DD where the OS records it, else empty.
	InstallDate string `json:"install_date"`
}

// List gathers installed programs via the OS-specific collector, sorted so
// the slice (and therefore its hash) is stable regardless of enumeration
// order.
//
// The bool is false when no collector ran: this platform has none, or no
// package manager was found. That is NOT the same as a host with zero
// programs, and the caller must not report a list in that case -- the
// server's reconcile marks everything absent from a reported list as
// removed, so a false "empty" would wipe a host's whole software history.
func List() ([]Program, bool) {
	progs, collected := list()
	if !collected {
		return nil, false
	}
	sort.Slice(progs, func(a, b int) bool {
		if progs[a].Name != progs[b].Name {
			return progs[a].Name < progs[b].Name
		}
		if progs[a].Source != progs[b].Source {
			return progs[a].Source < progs[b].Source
		}
		return progs[a].Version < progs[b].Version
	})
	return progs, true
}

// Hash is a stable digest of a sorted program list: the agent sends the list
// only when this changes from the hash it last stored.
func Hash(progs []Program) string {
	b, err := json.Marshal(progs)
	if err != nil {
		return fmt.Sprintf("unmarshalable-%v", err)
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:8])
}
