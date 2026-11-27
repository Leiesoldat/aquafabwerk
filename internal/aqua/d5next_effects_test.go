package aqua

import (
	"bufio"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// testdata/d5next_effects.txt: "<effect> <hex>" for each aquasuite config write (report 0x03,
// 809 bytes), in the order the effects were selected (base colour red).
func loadD5Effects(t *testing.T) ([]string, [][]byte) {
	return loadD5Fixture(t, "testdata/d5next_effects.txt")
}

func loadD5Fixture(t *testing.T, path string) ([]string, [][]byte) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var names []string
	var cfgs [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4096), 4096)
	for sc.Scan() {
		k, h, _ := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		b, err := hex.DecodeString(h)
		if err != nil {
			t.Fatalf("%s: bad hex", k)
		}
		if len(b) != d5nLen {
			t.Fatalf("%s: %d bytes, want %d", k, len(b), d5nLen)
		}
		names, cfgs = append(names, k), append(cfgs, b)
	}
	return names, cfgs
}

func d5FixCRC(b []byte) {
	c := CRC16USB(b[1:offCRC])
	b[offCRC], b[offCRC+1] = byte(c>>8), byte(c)
}

func d5CRCOK(b []byte) bool {
	return CRC16USB(b[1:offCRC]) == uint16(b[offCRC])<<8|uint16(b[offCRC+1])
}

// Applying each effect's block to the previous write must reproduce aquasuite's next write
// byte-for-byte: the effect lives entirely in [663:730).
func TestD5EffectBlocksReproduceCaptures(t *testing.T) {
	names, cfgs := loadD5Effects(t)
	for i, name := range names {
		if !d5CRCOK(cfgs[i]) {
			t.Errorf("%s: captured CRC invalid", name)
		}
		prev := cfgs[0]
		if i > 0 {
			prev = cfgs[i-1]
		}
		got := append([]byte(nil), prev...)
		if err := d5ApplyEffect(got, name, false, 0, 0, 0); err != nil {
			t.Fatal(err)
		}
		d5FixCRC(got)
		if string(got) != string(cfgs[i]) {
			t.Errorf("%s: patched config != captured write", name)
		}
		if Effects[name] != cfgs[i][offEffect] || d5EffectName(cfgs[i][offEffect]) != name {
			t.Errorf("%s: selector lookup mismatch (0x%02x)", name, cfgs[i][offEffect])
		}
	}
	if len(names) != len(D5NextEffects()) {
		t.Errorf("fixture has %d effects, table has %d", len(names), len(D5NextEffects()))
	}
}

// With the capture's red base colour, every effect whose primary slot held red (and every
// self-coloured effect) must reproduce the capture exactly; a blue base colour must only land
// in the primary slot (plus master brightness).
func TestD5EffectColour(t *testing.T) {
	names, cfgs := loadD5Effects(t)
	for i, name := range names {
		prev := cfgs[0]
		if i > 0 {
			prev = cfgs[i-1]
		}
		slot, coloured := d5Primary(name)
		off := offEffect + d5SlotBase + 4*slot
		if !coloured || string(cfgs[i][off:off+3]) == "\x00\x00\xff" {
			red := append([]byte(nil), prev...)
			_ = d5ApplyEffect(red, name, true, 255, 0, 0)
			d5FixCRC(red)
			if string(red) != string(cfgs[i]) {
				t.Errorf("%s: red base colour doesn't reproduce the capture", name)
			}
		}
		if !coloured {
			continue
		}
		got := append([]byte(nil), prev...)
		_ = d5ApplyEffect(got, name, true, 0, 0, 255)
		want := append([]byte(nil), prev...)
		_ = d5ApplyEffect(want, name, false, 0, 0, 0)
		if got[off] != 0x04 || got[off+1] != 0x00 || got[off+2] != 0xff {
			t.Errorf("%s: blue not in slot %d: % x", name, slot, got[off:off+4])
		}
		for j := range got {
			if (j < off || j >= off+3) && j != offBright && got[j] != want[j] {
				t.Errorf("%s: byte %d changed outside slot %d", name, j, slot)
			}
		}
	}
}

// Every advertised CoolerControl mode has a block, none of them is host-driven, and modes that
// take a colour have a primary slot.
func TestD5LightingModes(t *testing.T) {
	for _, m := range LightingModes() {
		if m.Name == "off" || m.Name == "status" {
			continue
		}
		if _, ok := d5EffectBlock(m.Name); !ok {
			t.Errorf("mode %s has no effect block", m.Name)
		}
		if d5HostDriven[m.Name] {
			t.Errorf("mode %s is host-driven", m.Name)
		}
		if _, ok := d5Primary(m.Name); ok != (m.MaxColors > 0) {
			t.Errorf("mode %s: MaxColors=%d but primary slot=%v", m.Name, m.MaxColors, ok)
		}
	}
}

// Strip capture (testdata/d5strip_effects.txt, same effect order): applying each effect to the
// strip block of the previous write must reproduce aquasuite's next write byte-for-byte, and
// strip writes must never touch the pump ring's block, brightness or on/off flag.
func TestD5StripReproducesCaptures(t *testing.T) {
	names, cfgs := loadD5Fixture(t, "testdata/d5strip_effects.txt")
	if len(names) != len(D5NextEffects()) {
		t.Fatalf("fixture has %d effects, table has %d", len(names), len(D5NextEffects()))
	}
	for i, name := range names {
		if !d5CRCOK(cfgs[i]) {
			t.Errorf("%s: captured CRC invalid", name)
		}
		prev := cfgs[0]
		if i > 0 {
			prev = cfgs[i-1]
		}
		for _, withColor := range []bool{false, true} {
			got := append([]byte(nil), prev...)
			if err := d5ApplyEffectOn(got, d5Strip, name, withColor, 255, 0, 0); err != nil {
				t.Fatal(err)
			}
			d5FixCRC(got)
			slot, coloured := d5Primary(name)
			off := offStripEffect + d5SlotBase + 4*slot
			if !withColor || !coloured || string(cfgs[i][off:off+4]) == "\x00\x00\xff\xff" {
				if string(got) != string(cfgs[i]) {
					t.Errorf("%s (withColor=%v): patched config != captured write", name, withColor)
				}
			}
			if string(got[offEffect:offEffect+d5BlockLen]) != string(prev[offEffect:offEffect+d5BlockLen]) ||
				got[offBright] != prev[offBright] || got[offOnOff] != prev[offOnOff] {
				t.Errorf("%s: strip write touched the pump ring", name)
			}
		}
		if d5EffectName(cfgs[i][offStripEffect]) != name {
			t.Errorf("%s: strip selector 0x%02x lookup failed", name, cfgs[i][offStripEffect])
		}
	}
}

// A strip colour lands entirely (hue, sat and value) in the effect's primary slot, and pump
// writes never touch the strip block.
func TestD5StripColourAndIsolation(t *testing.T) {
	_, cfgs := loadD5Fixture(t, "testdata/d5strip_effects.txt")
	base := cfgs[len(cfgs)-1]
	for _, name := range D5NextEffects() {
		slot, coloured := d5Primary(name)
		if coloured {
			got := append([]byte(nil), base...)
			_ = d5ApplyEffectOn(got, d5Strip, name, true, 0, 0, 128) // dim blue
			want := append([]byte(nil), base...)
			_ = d5ApplyEffectOn(want, d5Strip, name, false, 0, 0, 0)
			off := offStripEffect + d5SlotBase + 4*slot
			if got[off] != 0x04 || got[off+1] != 0x00 || got[off+2] != 0xff || got[off+3] != 0x80 {
				t.Errorf("%s: dim blue not in strip slot %d: % x", name, slot, got[off:off+4])
			}
			for j := range got {
				if (j < off || j >= off+4) && got[j] != want[j] {
					t.Errorf("%s: byte %d changed outside strip slot %d", name, j, slot)
				}
			}
		}
		pump := append([]byte(nil), base...)
		_ = d5ApplyEffect(pump, name, true, 0, 0, 255)
		if string(pump[offStripEffect:offStripEffect+d5BlockLen]) != string(base[offStripEffect:offStripEffect+d5BlockLen]) {
			t.Errorf("%s: pump write touched the strip block", name)
		}
	}
	for _, m := range StripLightingModes() {
		if m.Name == "status" {
			t.Error("strip offers status mode")
		}
	}
}
