package wasm

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/plugin/sdk"
)

// Bundle is the signed manifest file next to a module: <name>.plugin.json
// beside <name>.wasm.
type Bundle struct {
	Manifest  sdk.Manifest `json:"manifest"`
	Signature Signature    `json:"signature"`
}

// Signature is an ed25519 signature over the manifest, which carries the
// module's hash.
type Signature struct {
	Key string `json:"key"` // key id, see KeyID
	Sig string `json:"sig"` // base64, standard encoding
}

// domain separates plugin signatures from anything else the same key might sign
// (SBOMs).
const domain = "cernly plugin manifest v1\x00"

func message(m sdk.Manifest) ([]byte, error) {
	c, err := fact.Canonical(m)
	if err != nil {
		return nil, err
	}
	return append([]byte(domain), c...), nil
}

// KeyID names a public key: ed25519: and the first 16 hex digits of its
// SHA-256.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return "ed25519:" + hex.EncodeToString(sum[:8])
}

// Sign signs a manifest.
func Sign(priv ed25519.PrivateKey, m sdk.Manifest) (Signature, error) {
	msg, err := message(m)
	if err != nil {
		return Signature{}, err
	}
	pub := priv.Public().(ed25519.PublicKey)
	return Signature{Key: KeyID(pub), Sig: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg))}, nil
}

// Key is one trusted public key and who added it.
type Key struct {
	ID  string
	Pub ed25519.PublicKey
	By  string // "cernly" (built in) or "operator"
}

// Trust is the set of keys a module may be signed with: Cernly's built-in
// keys plus the keys the operator adds.
type Trust struct {
	keys map[string]Key
}

// TestKey is a fixed ed25519 key derived from a public seed, for tests and
// local builds. Anyone can sign with it, so nobody trusts it in production:
// it is never in the built-in keys, and a scan that trusts it records the
// signer as TestSigner(), so every report shows it.
func TestKey() ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("cernly plugin test key; never trust outside tests"))
	return ed25519.NewKeyFromSeed(seed[:])
}

// TestSignerPrefix marks the test key in a scan's plugins list.
const TestSignerPrefix = "test-key-not-for-production:"

// TestSigner is how a scan names the test key as signer.
func TestSigner() string { return TestSignerPrefix + KeyID(TestKey().Public().(ed25519.PublicKey)) }

// cernlyKeys are the public halves of Cernly's plugin signing keys, PEM (PKIX).
var cernlyKeys = []string{
	// cernly-plugins-2026, the offline release key (11 Nov 2026).
	`-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAjko5DFa9IkbTTo6cZcN3hv5GusfZM05FgxD0QE2ocL4=
-----END PUBLIC KEY-----
`,
}

// NewTrust returns Cernly's built-in keys plus the operator's PEM public
// keys.
func NewTrust(operator ...[]byte) (*Trust, error) {
	t := &Trust{keys: map[string]Key{}}
	for _, k := range cernlyKeys {
		pub, err := ParsePublicKey([]byte(k))
		if err != nil {
			return nil, fmt.Errorf("built-in key: %w", err)
		}
		if pub.Equal(TestKey().Public()) {
			return nil, errors.New("built-in key: the test key is never built in")
		}
		t.add(pub, "cernly")
	}
	for _, k := range operator {
		pub, err := ParsePublicKey(k)
		if err != nil {
			return nil, err
		}
		t.add(pub, "operator")
	}
	return t, nil
}

func (t *Trust) add(pub ed25519.PublicKey, by string) {
	id := KeyID(pub)
	if _, ok := t.keys[id]; !ok {
		t.keys[id] = Key{ID: id, Pub: pub, By: by}
	}
}

// Keys lists the trusted keys by id.
func (t *Trust) Keys() []Key {
	out := make([]Key, 0, len(t.keys))
	for _, k := range t.keys {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Verify checks a bundle's signature and returns the signing key's id.
func (t *Trust) Verify(b Bundle) (string, error) {
	if b.Signature.Key == "" || b.Signature.Sig == "" {
		return "", errors.New("unsigned")
	}
	k, ok := t.keys[b.Signature.Key]
	if !ok {
		return "", fmt.Errorf("signed by %s, which is not a trusted key", b.Signature.Key)
	}
	sig, err := base64.StdEncoding.DecodeString(b.Signature.Sig)
	if err != nil {
		return "", fmt.Errorf("signature: %w", err)
	}
	msg, err := message(b.Manifest)
	if err != nil {
		return "", err
	}
	if !ed25519.Verify(k.Pub, msg, sig) {
		return "", fmt.Errorf("signature by %s does not match the manifest", k.ID)
	}
	if k.Pub.Equal(TestKey().Public()) {
		return TestSigner(), nil
	}
	return k.ID, nil
}

// ParsePublicKey reads a PEM "PUBLIC KEY" (PKIX) holding an ed25519 key, as
// `openssl pkey -pubout` writes it.
func ParsePublicKey(data []byte) (ed25519.PublicKey, error) {
	blk, _ := pem.Decode(data)
	if blk == nil || blk.Type != "PUBLIC KEY" {
		return nil, errors.New("not a PEM PUBLIC KEY")
	}
	k, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("not an ed25519 key")
	}
	return pub, nil
}

// ParsePrivateKey reads a PEM "PRIVATE KEY" (PKCS #8) holding an ed25519
// key, as `openssl genpkey -algorithm ed25519` writes it.
func ParsePrivateKey(data []byte) (ed25519.PrivateKey, error) {
	blk, _ := pem.Decode(data)
	if blk == nil || blk.Type != "PRIVATE KEY" {
		return nil, errors.New("not a PEM PRIVATE KEY")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("not an ed25519 key")
	}
	return priv, nil
}

// EncodeKeys returns the PEM private and public key of priv.
func EncodeKeys(priv ed25519.PrivateKey) (privPEM, pubPEM []byte, err error) {
	p8, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	pk, err := x509.MarshalPKIXPublicKey(priv.Public())
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: p8}),
		pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pk}), nil
}

// ModuleHash is the hex SHA-256 of a module.
func ModuleHash(module []byte) string {
	sum := sha256.Sum256(module)
	return hex.EncodeToString(sum[:])
}

// trimWasm returns the bundle path's module path: x.plugin.json -> x.wasm.
func modulePath(bundlePath string) string {
	return strings.TrimSuffix(bundlePath, BundleSuffix) + ".wasm"
}

// BundleSuffix ends every signed manifest file.
const BundleSuffix = ".plugin.json"
