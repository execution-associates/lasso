package main

// Lasso's own age identity: <lassoDir>/age.txt on each host, generated the
// first time a bot there needs a secret, and the key every bot's fnox.toml
// points its "lasso" age provider at (key_file). One per install, never one
// baked into the binary — a shared key would be public and encrypt nothing.
// It is never replaced: every secret already encrypted to it would become
// unreadable.
//
// The format is age-keygen's (an X25519 scalar, bech32 "AGE-SECRET-KEY-1…",
// recipient "age1…"), produced here so a host needs no age binary.

import (
	"crypto/ecdh"
	"crypto/rand"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

func botAgeKeyPath(b Backend) string { return filepath.Join(lassoDirFor(b), "age.txt") }

// botAgeRecipient returns the host's lasso age recipient, creating the
// identity when there is none.
func botAgeRecipient(b Backend) (recipient, keyPath string, err error) {
	keyPath = botAgeKeyPath(b)
	if raw, err := b.ReadFile(keyPath); err == nil {
		r := ageRecipientFromFile(string(raw))
		if r == "" {
			return "", "", fmt.Errorf("%s has no public key line; it is not a key lasso can use", keyPath)
		}
		return r, keyPath, nil
	}
	identity, recipient, err := newAgeIdentity()
	if err != nil {
		return "", "", err
	}
	if err := b.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return "", "", err
	}
	body := fmt.Sprintf("# created: %s\n# public key: %s\n%s\n", time.Now().UTC().Format(time.RFC3339), recipient, identity)
	if err := b.WriteFile(keyPath, []byte(body), 0o600); err != nil {
		return "", "", err
	}
	return recipient, keyPath, nil
}

// ageRecipientFromFile reads the "# public key:" line age-keygen writes.
func ageRecipientFromFile(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "# public key:"); ok {
			v = strings.TrimSpace(v)
			if strings.HasPrefix(v, "age1") {
				return v
			}
		}
	}
	return ""
}

func newAgeIdentity() (identity, recipient string, err error) {
	var scalar [32]byte
	if _, err := rand.Read(scalar[:]); err != nil {
		return "", "", err
	}
	return ageKeyPair(scalar[:])
}

func ageKeyPair(scalar []byte) (identity, recipient string, err error) {
	priv, err := ecdh.X25519().NewPrivateKey(scalar)
	if err != nil {
		return "", "", err
	}
	id, err := bech32Encode("age-secret-key-", scalar)
	if err != nil {
		return "", "", err
	}
	rec, err := bech32Encode("age", priv.PublicKey().Bytes())
	if err != nil {
		return "", "", err
	}
	return strings.ToUpper(id), rec, nil
}

// --- bech32 (BIP 173), encode only -------------------------------------------

const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

func bech32Polymod(values []byte) uint32 {
	gen := [5]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	chk := uint32(1)
	for _, v := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := 0; i < 5; i++ {
			if (top>>i)&1 == 1 {
				chk ^= gen[i]
			}
		}
	}
	return chk
}

func bech32Encode(hrp string, data []byte) (string, error) {
	// 8-bit bytes to 5-bit groups, padded.
	var five []byte
	acc, bits := uint32(0), uint(0)
	for _, b := range data {
		acc = acc<<8 | uint32(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			five = append(five, byte(acc>>bits)&31)
		}
	}
	if bits > 0 {
		five = append(five, byte(acc<<(5-bits))&31)
	}
	var exp []byte
	for _, c := range hrp {
		exp = append(exp, byte(c)>>5)
	}
	exp = append(exp, 0)
	for _, c := range hrp {
		exp = append(exp, byte(c)&31)
	}
	mod := bech32Polymod(append(append(exp, five...), 0, 0, 0, 0, 0, 0)) ^ 1
	var sb strings.Builder
	sb.WriteString(hrp)
	sb.WriteByte('1')
	for _, v := range five {
		sb.WriteByte(bech32Charset[v])
	}
	for i := 0; i < 6; i++ {
		sb.WriteByte(bech32Charset[(mod>>uint(5*(5-i)))&31])
	}
	return sb.String(), nil
}
