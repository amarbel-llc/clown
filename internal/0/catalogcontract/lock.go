package catalogcontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"golang.org/x/crypto/blake2b"
)

// LockDigest is RFC 0018 §2's lock: the blake2b256 markl id of the canonical
// JSON of raw (a tool's MCP inputSchema). The catalog listing and every
// enforcer call this one function, so a lock is spelled the same by both
// (FDR 0028 rule 3).
func LockDigest(raw []byte) (string, error) {
	canon, err := CanonicalJSON(raw)
	if err != nil {
		return "", err
	}
	sum := blake2b.Sum256(canon)
	return blech32("blake2b256", sum[:]), nil
}

// CanonicalJSON re-serializes one JSON value with object keys sorted, no
// insignificant whitespace and strings and numbers as their JSON text (a
// number keeps its literal spelling; nothing is HTML-escaped).
func CanonicalJSON(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("canonical JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("canonical JSON: more than one value")
	}
	var buf bytes.Buffer
	if err := writeCanonical(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeString(buf, k); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := writeCanonical(buf, x[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case string:
		return writeString(buf, x)
	case json.Number:
		buf.WriteString(x.String())
	case bool:
		if x {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case nil:
		buf.WriteString("null")
	default:
		return fmt.Errorf("canonical JSON: unexpected %T", v)
	}
	return nil
}

func writeString(buf *bytes.Buffer, s string) error {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return err
	}
	buf.Truncate(buf.Len() - 1) // Encode's newline
	return nil
}

// blech32 is markl-id(7)'s encoding: BIP173 bech32 (not bech32m) with "-" as
// the separator, lower case, no length limit.
func blech32(hrp string, data []byte) string {
	const alphabet = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
	five := toFiveBit(data)
	values := append(hrpExpand(hrp), five...)
	polymod := bech32Polymod(append(values, 0, 0, 0, 0, 0, 0)) ^ 1
	out := make([]byte, 0, len(hrp)+1+len(five)+6)
	out = append(out, hrp...)
	out = append(out, '-')
	for _, v := range five {
		out = append(out, alphabet[v])
	}
	for i := 0; i < 6; i++ {
		out = append(out, alphabet[(polymod>>uint(5*(5-i)))&31])
	}
	return string(out)
}

func toFiveBit(data []byte) []byte {
	var out []byte
	acc, bits := 0, 0
	for _, b := range data {
		acc = (acc<<8 | int(b)) & 0xfff // at most 4 + 8 pending bits
		bits += 8
		for bits >= 5 {
			bits -= 5
			out = append(out, byte(acc>>bits&31))
		}
	}
	if bits > 0 {
		out = append(out, byte(acc<<(5-bits)&31))
	}
	return out
}

func hrpExpand(hrp string) []byte {
	out := make([]byte, 0, 2*len(hrp)+1)
	for i := 0; i < len(hrp); i++ {
		out = append(out, hrp[i]>>5)
	}
	out = append(out, 0)
	for i := 0; i < len(hrp); i++ {
		out = append(out, hrp[i]&31)
	}
	return out
}

func bech32Polymod(values []byte) uint32 {
	gen := [5]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	chk := uint32(1)
	for _, v := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := 0; i < 5; i++ {
			if top>>uint(i)&1 == 1 {
				chk ^= gen[i]
			}
		}
	}
	return chk
}
