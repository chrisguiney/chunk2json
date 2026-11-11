package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"

	gcbor "github.com/blinklabs-io/gouroboros/cbor"
)

// decodeCBOR walks raw CBOR into typed values rather than Go maps: Cardano
// CBOR has map keys that are arrays (unhashable in Go), byte strings must
// stay distinguishable from text for the labeller, and map key order is part
// of what someone inspecting a block wants to see.
//
// Value types: uint64, int64, string (text), bigNum, bytesV, []any, cborMap,
// tagV, bool, nil, float64/string (NaN/Inf), simpleV.
func decodeCBOR(data []byte) (any, error) {
	d := &walker{b: data}
	v, err := d.item(0)
	if err != nil {
		return nil, err
	}
	if d.p != len(d.b) {
		return nil, fmt.Errorf("%d trailing bytes", len(d.b)-d.p)
	}
	return v, nil
}

type bytesV []byte

func (b bytesV) MarshalJSON() ([]byte, error) { return json.Marshal(hex.EncodeToString(b)) }

// bigNum is an integer outside int64/uint64; it is emitted as a decimal string.
type bigNum struct{ *big.Int }

func (b bigNum) MarshalJSON() ([]byte, error) { return json.Marshal(b.String()) }

type entry struct{ K, V any }

// cborMap marshals as an object when every key is an int or text, and as a
// [{"k":..,"v":..}] list otherwise, since stringifying a structured key would
// lose its type.
type cborMap []entry

func (m cborMap) MarshalJSON() ([]byte, error) { return marshal(m) }

func (m cborMap) asJSON() any {
	for _, e := range m {
		switch e.K.(type) {
		case uint64, int64, string:
		default:
			out := make([]any, len(m))
			for i, e := range m {
				out[i] = obj{{"k", e.K}, {"v", e.V}}
			}
			return out
		}
	}
	o := make(obj, len(m))
	for i, e := range m {
		o[i] = kv{fmt.Sprint(e.K), e.V}
	}
	return o
}

type tagV struct {
	Num uint64
	V   any
}

func (t tagV) MarshalJSON() ([]byte, error) { return marshal(t) }

type simpleV uint64

func (s simpleV) MarshalJSON() ([]byte, error) { return marshal(s) }

type kv struct {
	k string
	v any
}

// obj is a JSON object that keeps its field order.
type obj []kv

func (o obj) MarshalJSON() ([]byte, error) { return marshal(o) }

// marshal encodes v, writing this file's container types (obj, []any,
// cborMap, tags) itself and handing only leaves and view structs to
// encoding/json. encoding/json counts each nested MarshalJSON call toward its
// depth limit, which deeply nested on-chain metadata exceeds; walking the
// containers here keeps them out of that count.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeJSON(&buf, v, 0); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeJSON(buf *bytes.Buffer, v any, depth int) error {
	if depth > maxDepth {
		return errors.New("json: nesting too deep")
	}
	switch x := v.(type) {
	case obj:
		buf.WriteByte('{')
		for i, e := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeLeaf(buf, e.k); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := writeJSON(buf, e.v, depth+1); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
		return nil
	case []any:
		buf.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeJSON(buf, e, depth+1); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil
	case cborMap:
		return writeJSON(buf, x.asJSON(), depth)
	case tagV:
		return writeJSON(buf, obj{{"@tag", x.Num}, {"@value", x.V}}, depth)
	case simpleV:
		return writeJSON(buf, obj{{"@simple", uint64(x)}}, depth)
	case bytesV:
		return writeLeaf(buf, hex.EncodeToString(x))
	case bigNum:
		return writeLeaf(buf, x.String())
	default:
		return writeLeaf(buf, v)
	}
}

func writeLeaf(buf *bytes.Buffer, v any) error {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	buf.Truncate(buf.Len() - 1) // Encode appends '\n'
	return nil
}

// maxDepth bounds recursion on hostile input. It is gouroboros's decoder
// limit, so the wire views accept exactly the blocks the struct view can
// decode; real chain data comes close (one Preview block nests 16,056 levels).
var maxDepth = gcbor.MaxNestedLevels

var errBreak = errors.New("break")

type walker struct {
	b []byte
	p int
}

func (d *walker) need(n uint64) error {
	if uint64(len(d.b)-d.p) < n {
		return fmt.Errorf("truncated at offset %d", d.p)
	}
	return nil
}

// head returns major type, additional info and argument; indefinite length
// reports ai 31.
func (d *walker) head() (byte, byte, uint64, error) {
	if err := d.need(1); err != nil {
		return 0, 0, 0, err
	}
	ib := d.b[d.p]
	d.p++
	mt, ai := ib>>5, ib&0x1f
	var n int
	switch {
	case ai < 24:
		return mt, ai, uint64(ai), nil
	case ai == 24:
		n = 1
	case ai == 25:
		n = 2
	case ai == 26:
		n = 4
	case ai == 27:
		n = 8
	case ai == 31:
		return mt, ai, 0, nil
	default:
		return 0, 0, 0, fmt.Errorf("reserved additional info %d at offset %d", ai, d.p-1)
	}
	if err := d.need(uint64(n)); err != nil {
		return 0, 0, 0, err
	}
	var arg uint64
	for _, c := range d.b[d.p : d.p+n] {
		arg = arg<<8 | uint64(c)
	}
	d.p += n
	return mt, ai, arg, nil
}

func (d *walker) str(mt, ai byte, arg uint64) ([]byte, error) {
	if ai != 31 {
		if err := d.need(arg); err != nil {
			return nil, err
		}
		s := d.b[d.p : d.p+int(arg)]
		d.p += int(arg)
		return s, nil
	}
	var out []byte
	for {
		cmt, cai, carg, err := d.head()
		if err != nil {
			return nil, err
		}
		if cmt == 7 && cai == 31 {
			return out, nil
		}
		if cmt != mt || cai == 31 {
			return nil, fmt.Errorf("bad indefinite string chunk at offset %d", d.p)
		}
		s, err := d.str(cmt, cai, carg)
		if err != nil {
			return nil, err
		}
		out = append(out, s...)
	}
}

// jsonDepthCut is the CBOR nesting depth below which a subtree is printed as
// {"@too_deep_cbor": "<hex>"} instead of being expanded. JSON parsers,
// encoding/json and jq included, refuse documents nested more than 10,000
// levels, and each CBOR level can cost two JSON levels. On-chain metadata
// does reach this: one Preview transaction nests over 16,000 levels.
const jsonDepthCut = 4096

func (d *walker) item(depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("nesting too deep")
	}
	if depth == jsonDepthCut {
		start := d.p
		v, err := d.item(depth + 1)
		if err != nil {
			return v, err // includes errBreak, which must reach the enclosing container
		}
		return obj{{"@too_deep_cbor", bytesV(d.b[start:d.p])}}, nil
	}
	mt, ai, arg, err := d.head()
	if err != nil {
		return nil, err
	}
	if ai == 31 && mt < 2 {
		return nil, fmt.Errorf("indefinite length on major type %d", mt)
	}
	switch mt {
	case 0:
		return arg, nil
	case 1:
		if arg <= math.MaxInt64 {
			return -1 - int64(arg), nil
		}
		n := new(big.Int).SetUint64(arg)
		return bigNum{n.Neg(n).Sub(n, big.NewInt(1))}, nil
	case 2:
		s, err := d.str(mt, ai, arg)
		return bytesV(s), err
	case 3:
		s, err := d.str(mt, ai, arg)
		return string(s), err
	case 4:
		out := []any{}
		for i := uint64(0); ai == 31 || i < arg; i++ {
			v, err := d.item(depth + 1)
			if errors.Is(err, errBreak) && ai == 31 {
				break
			}
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case 5:
		m := cborMap{}
		for i := uint64(0); ai == 31 || i < arg; i++ {
			k, err := d.item(depth + 1)
			if errors.Is(err, errBreak) && ai == 31 {
				break
			}
			if err != nil {
				return nil, err
			}
			v, err := d.item(depth + 1)
			if err != nil {
				return nil, err
			}
			m = append(m, entry{k, v})
		}
		return m, nil
	case 6:
		v, err := d.item(depth + 1)
		if err != nil {
			return nil, err
		}
		if b, ok := v.(bytesV); ok && (arg == 2 || arg == 3) {
			n := new(big.Int).SetBytes(b)
			if arg == 3 {
				n.Neg(n).Sub(n, big.NewInt(1))
			}
			if n.IsUint64() {
				return n.Uint64(), nil
			}
			if n.IsInt64() {
				return n.Int64(), nil
			}
			return bigNum{n}, nil
		}
		return tagV{arg, v}, nil
	default: // 7
		switch {
		case ai == 31:
			return nil, errBreak
		case ai == 20:
			return false, nil
		case ai == 21:
			return true, nil
		case ai == 22, ai == 23:
			return nil, nil
		case ai == 25:
			return floatOut(float64(halfToFloat(uint16(arg)))), nil
		case ai == 26:
			return floatOut(float64(math.Float32frombits(uint32(arg)))), nil
		case ai == 27:
			return floatOut(math.Float64frombits(arg)), nil
		default:
			return simpleV(arg), nil
		}
	}
}

func floatOut(f float64) any {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return f
}

func halfToFloat(h uint16) float32 {
	sign := uint32(h>>15) << 31
	exp := uint32(h>>10) & 0x1f
	frac := uint32(h & 0x3ff)
	switch exp {
	case 0:
		f := float32(frac) / 1024 * float32(math.Pow(2, -14))
		if sign != 0 {
			f = -f
		}
		return f
	case 0x1f:
		return math.Float32frombits(sign | 0xff<<23 | frac<<13)
	}
	return math.Float32frombits(sign | (exp+112)<<23 | frac<<13)
}
