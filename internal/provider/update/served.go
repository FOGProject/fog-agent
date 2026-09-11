package update

import "encoding/base64"

// inline returns the manifest and envelope the server sent, when it sent
// both and both decode. Anything less is ignored rather than refused: a
// server that could not send a usable pair has told the agent nothing, and
// the manifest URL is still there to ask. Bytes over maxManifest are
// ignored the same way, because the size limit comes before the trust.
func inline(d Desired) (manifest, envelope []byte, ok bool) {
	if d.Manifest == "" || d.Signature == "" {
		return nil, nil, false
	}
	m, err := base64.StdEncoding.DecodeString(d.Manifest)
	if err != nil || len(m) > maxManifest {
		return nil, nil, false
	}
	e, err := base64.StdEncoding.DecodeString(d.Signature)
	if err != nil || len(e) > maxManifest {
		return nil, nil, false
	}
	return m, e, true
}
