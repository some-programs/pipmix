package pw

import (
	"math"
	"slices"
	"testing"
)

func TestPodRoundTrip(t *testing.T) {
	var b builder
	b.structure(func() {
		b.int(-7)
		b.id(42)
		b.bool(true)
		b.long(1 << 40)
		b.string("héllo")
		b.none()
		b.floatArray([]float32{0.5, 1})
		b.idArray([]uint32{3, 4})
		b.dict([][2]string{{"a", "b"}, {"key", ""}})
		b.object(objectProps, paramProps, func() {
			b.prop(propMute)
			b.bool(true)
			b.prop(propChannelVolumes)
			b.floatArray([]float32{0.25})
		})
	})
	if len(b.buf)%8 != 0 {
		t.Fatalf("encoded length %d is not a multiple of 8", len(b.buf))
	}

	p, rest, err := readPod(b.buf)
	if err != nil || len(rest) != 0 {
		t.Fatalf("readPod: %v, %d bytes left", err, len(rest))
	}
	f, err := p.fields()
	if err != nil {
		t.Fatal(err)
	}
	if v := f.int(); v != -7 {
		t.Errorf("int = %d", v)
	}
	if v := f.u32(); v != 42 {
		t.Errorf("id = %d", v)
	}
	if v, _ := f.pod().bool(); !v {
		t.Errorf("bool = false")
	}
	if v := f.long(); v != 1<<40 {
		t.Errorf("long = %d", v)
	}
	if v := f.string(); v != "héllo" {
		t.Errorf("string = %q", v)
	}
	if v := f.string(); v != "" {
		t.Errorf("None as string = %q", v)
	}
	if v, err := f.pod().floats(); err != nil || !slices.Equal(v, []float32{0.5, 1}) {
		t.Errorf("floats = %v, %v", v, err)
	}
	if v, err := f.pod().u32s(); err != nil || !slices.Equal(v, []uint32{3, 4}) {
		t.Errorf("ids = %v, %v", v, err)
	}
	if d := f.dict(); len(d) != 2 || d["a"] != "b" || d["key"] != "" {
		t.Errorf("dict = %v", d)
	}
	o, err := f.pod().object()
	if err != nil {
		t.Fatal(err)
	}
	if o.typ != objectProps || o.id != paramProps {
		t.Errorf("object type %#x id %d", o.typ, o.id)
	}
	if v, _ := o.props[propMute].bool(); !v {
		t.Errorf("mute = false")
	}
	if v, _ := o.props[propChannelVolumes].floats(); !slices.Equal(v, []float32{0.25}) {
		t.Errorf("channelVolumes = %v", v)
	}
	if f.err != nil || f.more() {
		t.Errorf("parser: err %v, more %v", f.err, f.more())
	}
}

func TestParserTruncated(t *testing.T) {
	var b builder
	b.structure(func() {
		b.int(1)
		b.string("x")
	})
	p, _, err := readPod(b.buf)
	if err != nil {
		t.Fatal(err)
	}
	p.body = p.body[:len(p.body)-8] // cuts into the string, not just its padding
	f, _ := p.fields()
	f.int()
	f.string()
	if f.err == nil {
		t.Error("no error for a truncated struct")
	}
}

func TestChoiceValue(t *testing.T) {
	// Choice(None, Int 5, alternatives...) reads as its first value.
	var b builder
	start := b.begin(typeChoice)
	b.u32(0) // SPA_CHOICE_None
	b.u32(0)
	b.header(4, typeInt)
	b.u32(5)
	b.u32(9)
	b.end(start)
	b.pad()
	p, _, err := readPod(b.buf)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := p.int(); err != nil || v != 5 {
		t.Errorf("int = %d, %v", v, err)
	}
}

func TestIDMap(t *testing.T) {
	m := idMap{next: 2}
	a, b, c := m.alloc(), m.alloc(), m.alloc()
	if a != 2 || b != 3 || c != 4 {
		t.Fatalf("alloc = %d %d %d", a, b, c)
	}
	m.release(b)
	m.release(a)
	m.release(a)  // twice: ignored
	m.release(1)  // reserved: ignored
	m.release(99) // never allocated: ignored
	if x, y, z := m.alloc(), m.alloc(), m.alloc(); x != a || y != b || z != 5 {
		t.Errorf("alloc after release = %d %d %d, want %d %d 5", x, y, z, a, b)
	}
}

func TestChangedParams(t *testing.T) {
	old := map[uint32]uint32{paramProps: paramInfoRead}
	same := map[uint32]uint32{paramProps: paramInfoRead}
	flipped := map[uint32]uint32{paramProps: paramInfoRead | 1}
	unreadable := map[uint32]uint32{paramProps: 1}
	if got := changedParams(nil, same, paramProps); !slices.Equal(got, []uint32{paramProps}) {
		t.Errorf("first info: %v", got)
	}
	if got := changedParams(old, same, paramProps); got != nil {
		t.Errorf("unchanged: %v", got)
	}
	if got := changedParams(old, flipped, paramProps); !slices.Equal(got, []uint32{paramProps}) {
		t.Errorf("serial flipped: %v", got)
	}
	if got := changedParams(old, unreadable, paramProps); got != nil {
		t.Errorf("unreadable: %v", got)
	}
}

func TestStep(t *testing.T) {
	for _, tc := range []struct{ c, delta, want float64 }{
		{1.02, -0.05, 1},
		{0.98, 0.05, 1},
		{1, 0.05, 1.05},
		{1, -0.05, 0.95},
		{0.9999999, 0.05, 1.05}, // 100% after a float32 round trip
		{1.0000001, -0.05, 0.95},
		{0.97, 0.01, 0.98},
		{0.975, 0.01, 0.98},
		{0.03, -0.05, 0},
		{0, -0.05, -0.05}, // clamped by the caller
	} {
		if got := step(tc.c, tc.delta); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("step(%v, %v) = %v, want %v", tc.c, tc.delta, got, tc.want)
		}
	}
}
