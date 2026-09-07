package release

import (
	"crypto/x509"
	_ "embed"
	"encoding/pem"
	"sync"
)

// rootsPEM is the FOG signing root (or roots) this build trusts to have
// released a version of the agent. Compiled in rather than read from disk
// on purpose: a trust anchor a local administrator can edit is not a trust
// anchor, and the whole design rests on this being a property of the
// binary rather than of the machine it landed on.
//
// The committed file carries no certificates, so a build made from a plain
// checkout trusts nothing and refuses every update -- which is the correct
// default for a build nobody signed for. A release build, and a lab build,
// replace the contents before compiling. See build/mint-signing-ca.sh.
//
//go:embed roots.pem
var rootsPEM []byte

var (
	rootsOnce sync.Once
	rootPool  *x509.CertPool
	rootCount int
)

// Roots is the pool Verify is called with, and how many certificates are
// in it. A caller that gets 0 must not treat self-update as available:
// there is nothing that could ever verify, so every attempt would refuse,
// and reporting that once as "this build carries no signing root" is far
// more useful to an admin than a signature failure per poll.
func Roots() (*x509.CertPool, int) {
	rootsOnce.Do(func() {
		rootPool = x509.NewCertPool()
		before := 0
		// AppendCertsFromPEM reports only whether it added anything, so
		// the count comes from parsing rather than from its answer.
		for _, c := range parsePEMCerts(rootsPEM) {
			rootPool.AddCert(c)
			before++
		}
		rootCount = before
	})
	return rootPool, rootCount
}

// parsePEMCerts returns every certificate in b, ignoring comments, blank
// lines and any block that is not a certificate. A roots file is edited by
// hand or concatenated by a script, so it is expected to have text around
// the blocks.
func parsePEMCerts(b []byte) []*x509.Certificate {
	var out []*x509.Certificate
	for {
		blk, rest := pem.Decode(b)
		if blk == nil {
			return out
		}
		b = rest
		if blk.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			continue
		}
		out = append(out, c)
	}
}
