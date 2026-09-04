// Package inventory gathers the host's hardware facts and marshals them to
// the field names FOG's existing `inventory` table uses (design 0006), so
// the server writes them straight onto the one inventory row per host. These
// are facts, not desired state: they ride the poll request when their hash
// changes, never the poll answer.
package inventory

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// Inventory is a complete hardware snapshot. Every field is always sent (no
// omitempty): the block is the whole row, and an unreadable field is an
// explicit empty string, not an absent one, so the server writes a full row
// each time rather than merging against stale values.
type Inventory struct {
	SysMan     string `json:"sysman"`
	SysProduct string `json:"sysproduct"`
	SysVersion string `json:"sysversion"`
	SysSerial  string `json:"sysserial"`
	SysUUID    string `json:"sysuuid"`
	SysType    string `json:"systype"`

	BIOSVendor  string `json:"biosvendor"`
	BIOSVersion string `json:"biosversion"`
	BIOSDate    string `json:"biosdate"`

	MBMan         string `json:"mbman"`
	MBProductName string `json:"mbproductname"`
	MBVersion     string `json:"mbversion"`
	MBSerial      string `json:"mbserial"`
	MBAsset       string `json:"mbasset"`

	CPUMan     string `json:"cpuman"`
	CPUVersion string `json:"cpuversion"`
	CPUCurrent string `json:"cpucurrent"`
	CPUMax     string `json:"cpumax"`

	// Mem is total physical memory in megabytes, as a decimal string. The
	// server's Inventory::getMem() formats it for display; the lab check
	// confirms the unit matches what that method expects.
	Mem string `json:"mem"`

	// The inventory table holds one disk; the agent reports the primary.
	// Multiple disks are a normalized relation later (0007), not this row.
	HDModel    string `json:"hdmodel"`
	HDSerial   string `json:"hdserial"`
	HDFirmware string `json:"hdfirmware"`

	CaseMan    string `json:"caseman"`
	CaseVer    string `json:"casever"`
	CaseSerial string `json:"caseserial"`
	CaseAsset  string `json:"caseasset"`

	// GPUVendors and GPUProducts are comma-joined, matching how the table
	// and its UI split them; a normalized relation is 0007.
	GPUVendors  string `json:"gpuvendors"`
	GPUProducts string `json:"gpuproducts"`
}

// Gather reads the host's hardware via the OS-specific collector. The bool
// is false when this platform has no collector, or when the collector read
// nothing at all: the caller must then send no inventory block, because an
// empty block would overwrite a good server-side row with blanks.
func Gather() (Inventory, bool) { return gather() }

// Hash is a stable digest of the snapshot: the agent sends the block only
// when this changes from the hash it last stored. Marshaling a struct is
// deterministic in Go (field order is fixed), so the digest is stable across
// runs without a separate canonicalization step.
func (i Inventory) Hash() string {
	b, err := json.Marshal(i)
	if err != nil {
		// A struct of strings does not fail to marshal; if it somehow did,
		// a changing hash forces a resend rather than hiding the fault.
		return fmt.Sprintf("unmarshalable-%v", err)
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:8])
}
