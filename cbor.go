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

func (m cborMap) MarshalJSON() ([]byte, error) {
	simple := true
	for _, e := range m {
		switch e.K.(type) {
		case uint64, int64, string:
		default:
			simple = false
		}
	}
	if simple {
		o := make(obj, len(m))
		for i, e := range m {
			o[i] = kv{fmt.Sprint(e.K), e.V}
		}
		return o.MarshalJSON()
	}
	out := make([]any, len(m))
	for i, e := range m {
		out[i] = obj{{"k", e.K}, {"v", e.V}}
	}
	return marshal(out)
}

type tagV struct {
	Num uint64
	V   any
}

func (t tagV) MarshalJSON() ([]byte, error) {
	return obj{{"@tag", t.Num}, {"@value", t.V}}.MarshalJSON()
}

type simpleV uint64

func (s simpleV) MarshalJSON() ([]byte, error) { return obj{{"@simple", uint64(s)}}.MarshalJSON() }

type kv struct {
	k string
	v any
}

// obj is a JSON object that keeps its field order.
type obj []kv

func (o obj) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, e := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, _ := json.Marshal(e.k)
		buf.Write(k)
		buf.WriteByte(':')
		v, err := marshal(e.v)
		if err != nil {
			return nil, err
		}
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte{'\n'}), nil
}

const maxDepth = 512

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

func (d *walker) item(depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("nesting too deep")
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
