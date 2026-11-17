package pw

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
)

// SPA POD value types (enum spa_type).
const (
	typeNone   = 1
	typeBool   = 2
	typeID     = 3
	typeInt    = 4
	typeLong   = 5
	typeFloat  = 6
	typeDouble = 7
	typeString = 8
	typeArray  = 13
	typeStruct = 14
	typeObject = 15
	typeChoice = 19
)

var native = binary.NativeEndian

// pad8 rounds n up to a multiple of 8, the alignment of every POD.
func pad8(n int) int { return (n + 7) &^ 7 }

// A builder encodes PODs. Containers are written with begin and end, which
// patches the container's size once its contents are known.
type builder struct {
	buf []byte
}

func (b *builder) u32(v uint32) { b.buf = native.AppendUint32(b.buf, v) }

func (b *builder) header(size int, typ uint32) {
	b.u32(uint32(size))
	b.u32(typ)
}

func (b *builder) pad() {
	for len(b.buf)%8 != 0 {
		b.buf = append(b.buf, 0)
	}
}

func (b *builder) none() { b.header(0, typeNone) }

func (b *builder) bool(v bool) {
	b.header(4, typeBool)
	if v {
		b.u32(1)
	} else {
		b.u32(0)
	}
	b.pad()
}

func (b *builder) id(v uint32) {
	b.header(4, typeID)
	b.u32(v)
	b.pad()
}

func (b *builder) int(v int32) {
	b.header(4, typeInt)
	b.u32(uint32(v))
	b.pad()
}

func (b *builder) long(v int64) {
	b.header(8, typeLong)
	b.buf = native.AppendUint64(b.buf, uint64(v))
}

func (b *builder) string(s string) {
	s = strings.ReplaceAll(s, "\x00", "")
	b.header(len(s)+1, typeString)
	b.buf = append(b.buf, s...)
	b.buf = append(b.buf, 0)
	b.pad()
}

func (b *builder) floatArray(vs []float32) {
	b.header(8+4*len(vs), typeArray)
	b.header(4, typeFloat)
	for _, v := range vs {
		b.u32(math.Float32bits(v))
	}
	b.pad()
}

func (b *builder) idArray(vs []uint32) {
	b.header(8+4*len(vs), typeArray)
	b.header(4, typeID)
	for _, v := range vs {
		b.u32(v)
	}
	b.pad()
}

// begin starts a container of type typ and returns the offset end needs.
func (b *builder) begin(typ uint32) int {
	start := len(b.buf)
	b.header(0, typ)
	return start
}

func (b *builder) end(start int) {
	native.PutUint32(b.buf[start:], uint32(len(b.buf)-start-8))
}

// structure writes a Struct holding whatever fill writes.
func (b *builder) structure(fill func()) {
	start := b.begin(typeStruct)
	fill()
	b.end(start)
}

// object writes an Object of type typ and id. fill adds its properties
// with prop.
func (b *builder) object(typ, id uint32, fill func()) {
	start := b.begin(typeObject)
	b.u32(typ)
	b.u32(id)
	fill()
	b.end(start)
}

// prop starts an object property; the value is written next.
func (b *builder) prop(key uint32) {
	b.u32(key)
	b.u32(0)
}

// dict writes a dictionary the way the protocol does:
// Struct(Int n, (String key, String value)*).
func (b *builder) dict(kv [][2]string) {
	b.structure(func() {
		b.int(int32(len(kv)))
		for _, e := range kv {
			b.string(e[0])
			b.string(e[1])
		}
	})
}

var errShort = errors.New("pod: truncated")

// A pod is one decoded POD: its type and its body, without padding.
type pod struct {
	typ  uint32
	body []byte
}

// readPod splits the first POD off b.
func readPod(b []byte) (pod, []byte, error) {
	if len(b) < 8 {
		return pod{}, nil, errShort
	}
	size := int(native.Uint32(b))
	typ := native.Uint32(b[4:])
	if size > len(b)-8 {
		return pod{}, nil, errShort
	}
	p := pod{typ: typ, body: b[8 : 8+size]}
	// The last POD of a message may lack its padding.
	return p, b[min(pad8(8+size), len(b)):], nil
}

func (p pod) typeError(want string) error {
	return fmt.Errorf("pod: want %s, have type %d", want, p.typ)
}

// value returns p, or a Choice's current value, which is its first one.
func (p pod) value() pod {
	if p.typ != typeChoice || len(p.body) < 16 {
		return p
	}
	size := int(native.Uint32(p.body[8:]))
	typ := native.Uint32(p.body[12:])
	if size > len(p.body)-16 {
		return p
	}
	return pod{typ: typ, body: p.body[16 : 16+size]}
}

func (p pod) u32() (uint32, error) {
	p = p.value()
	switch p.typ {
	case typeBool, typeID, typeInt:
		if len(p.body) < 4 {
			return 0, errShort
		}
		return native.Uint32(p.body), nil
	}
	return 0, p.typeError("Int or Id")
}

func (p pod) int() (int32, error) {
	v, err := p.u32()
	return int32(v), err
}

func (p pod) bool() (bool, error) {
	v, err := p.u32()
	return v != 0, err
}

func (p pod) long() (int64, error) {
	p = p.value()
	if p.typ != typeLong {
		return 0, p.typeError("Long")
	}
	if len(p.body) < 8 {
		return 0, errShort
	}
	return int64(native.Uint64(p.body)), nil
}

func (p pod) float() (float32, error) {
	p = p.value()
	switch p.typ {
	case typeFloat:
		if len(p.body) < 4 {
			return 0, errShort
		}
		return math.Float32frombits(native.Uint32(p.body)), nil
	case typeDouble:
		if len(p.body) < 8 {
			return 0, errShort
		}
		return float32(math.Float64frombits(native.Uint64(p.body))), nil
	}
	return 0, p.typeError("Float")
}

// string returns a String's value. None, the protocol's null string, is "".
func (p pod) string() (string, error) {
	switch p.typ {
	case typeNone:
		return "", nil
	case typeString:
		s, _, _ := strings.Cut(string(p.body), "\x00")
		return s, nil
	}
	return "", p.typeError("String")
}

// array returns an Array's element type and its elements' bytes.
func (p pod) array() (typ uint32, elems [][]byte, err error) {
	p = p.value()
	if p.typ != typeArray {
		return 0, nil, p.typeError("Array")
	}
	if len(p.body) < 8 {
		return 0, nil, errShort
	}
	size := int(native.Uint32(p.body))
	typ = native.Uint32(p.body[4:])
	if size == 0 {
		return typ, nil, nil
	}
	data := p.body[8:]
	for len(data) >= size {
		elems = append(elems, data[:size])
		data = data[size:]
	}
	return typ, elems, nil
}

func (p pod) floats() ([]float32, error) {
	typ, elems, err := p.array()
	if err != nil {
		return nil, err
	}
	if typ != typeFloat {
		return nil, p.typeError("Array of Float")
	}
	vs := make([]float32, 0, len(elems))
	for _, e := range elems {
		vs = append(vs, math.Float32frombits(native.Uint32(e)))
	}
	return vs, nil
}

// u32s returns the elements of an Array of Id or Int.
func (p pod) u32s() ([]uint32, error) {
	typ, elems, err := p.array()
	if err != nil {
		return nil, err
	}
	if typ != typeID && typ != typeInt {
		return nil, p.typeError("Array of Id or Int")
	}
	vs := make([]uint32, 0, len(elems))
	for _, e := range elems {
		vs = append(vs, native.Uint32(e))
	}
	return vs, nil
}

func (p pod) ints() ([]int32, error) {
	us, err := p.u32s()
	vs := make([]int32, len(us))
	for i, u := range us {
		vs[i] = int32(u)
	}
	return vs, err
}

// fields returns a parser over a Struct's fields.
func (p pod) fields() (*parser, error) {
	if p.typ != typeStruct {
		return nil, p.typeError("Struct")
	}
	return &parser{b: p.body}, nil
}

// An object is a decoded Object POD.
type object struct {
	typ, id uint32
	props   map[uint32]pod
}

func (p pod) object() (object, error) {
	if p.typ != typeObject {
		return object{}, p.typeError("Object")
	}
	if len(p.body) < 8 {
		return object{}, errShort
	}
	o := object{
		typ:   native.Uint32(p.body),
		id:    native.Uint32(p.body[4:]),
		props: make(map[uint32]pod),
	}
	b := p.body[8:]
	for len(b) >= 8 {
		key := native.Uint32(b)
		v, rest, err := readPod(b[8:])
		if err != nil {
			return o, err
		}
		o.props[key] = v
		b = rest
	}
	return o, nil
}

// A parser reads the fields of a Struct in order. The first error sticks:
// later reads return zero values, and err reports it.
type parser struct {
	b   []byte
	err error
}

func (p *parser) next() pod {
	if p.err != nil {
		return pod{}
	}
	v, rest, err := readPod(p.b)
	if err != nil {
		p.err = err
		return pod{}
	}
	p.b = rest
	return v
}

func (p *parser) check(err error) {
	if p.err == nil {
		p.err = err
	}
}

func (p *parser) more() bool { return p.err == nil && len(p.b) >= 8 }

func (p *parser) pod() pod { return p.next() }

func (p *parser) u32() uint32 {
	v, err := p.next().u32()
	p.check(err)
	return v
}

func (p *parser) int() int32 {
	v, err := p.next().int()
	p.check(err)
	return v
}

func (p *parser) long() int64 {
	v, err := p.next().long()
	p.check(err)
	return v
}

func (p *parser) string() string {
	v, err := p.next().string()
	p.check(err)
	return v
}

// fields returns a parser over the next field, a Struct.
func (p *parser) fields() *parser {
	f, err := p.next().fields()
	if err != nil {
		p.check(err)
		return &parser{err: p.err}
	}
	return f
}

// dict reads a dictionary, Struct(Int n, (String key, String value)*).
func (p *parser) dict() map[string]string {
	f := p.fields()
	n := f.int()
	m := make(map[string]string, max(n, 0))
	for range n {
		k := f.string()
		v := f.string()
		if f.err != nil {
			break
		}
		m[k] = v
	}
	p.check(f.err)
	return m
}

// paramInfo reads the param list of a Node::info or Device::info event,
// Struct(Int n, (Id id, Int flags)*), as a map from id to flags.
func (p *parser) paramInfo() map[uint32]uint32 {
	f := p.fields()
	n := f.int()
	m := make(map[uint32]uint32, max(n, 0))
	for range n {
		id := f.u32()
		flags := f.u32()
		if f.err != nil {
			break
		}
		m[id] = flags
	}
	p.check(f.err)
	return m
}
