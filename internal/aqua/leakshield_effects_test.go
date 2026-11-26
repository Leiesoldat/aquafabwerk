package aqua

import (
	"bufio"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// testdata/leakshield_effects.txt: "base <hex>" (the static red config before the run) then
// "<effect> <hex>" for each aquasuite write, in the order the effects were selected.
func loadLSEffects(t *testing.T) (map[string][]byte, []string) {
	t.Helper()
	f, err := os.Open("testdata/leakshield_effects.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, order := map[string][]byte{}, []string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4096), 4096)
	for sc.Scan() {
		k, h, _ := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		b, err := hex.DecodeString(h)
		if err != nil || len(b) != lsLen {
			t.Fatalf("%s: bad fixture", k)
		}
		m[k] = b
		order = append(order, k)
	}
	return m, order
}

func lsFixCRC(b []byte) {
	c := CRC16USB(b[1:lsOffCRC])
	b[lsOffCRC], b[lsOffCRC+1] = byte(c>>8), byte(c)
}

// Applying an effect's captured block to the pre-run config must reproduce aquasuite's write
// byte-for-byte: the effect lives entirely in [577:644].
func TestLeakshieldEffectBlocksReproduceCaptures(t *testing.T) {
	fx, order := loadLSEffects(t)
	for _, name := range order[1:] {
		got := append([]byte(nil), fx["base"]...)
		if err := lsApplyEffect(got, name, false, 0, 0, 0); err != nil {
			t.Fatal(err)
		}
		lsFixCRC(got)
		if string(got) != string(fx[name]) {
			t.Errorf("%s: patched config != captured write", name)
		}
		if !lsCRCOK(fx[name]) {
			t.Errorf("%s: captured CRC invalid", name)
		}
	}
	if len(order)-1 != len(lsEffectBlocks) {
		t.Errorf("fixture has %d effects, table has %d", len(order)-1, len(lsEffectBlocks))
	}
}

// The capture used red as the base colour, so for effects whose primary slot held red,
// applying red must also reproduce the capture exactly — and the colour must only ever land
// in the primary slot.
func TestLeakshieldEffectPrimarySlot(t *testing.T) {
	fx, _ := loadLSEffects(t)
	for name, slot := range lsPrimarySlot {
		off := lsOffHue + 4*slot
		got := append([]byte(nil), fx["base"]...)
		if err := lsApplyEffect(got, name, true, 0, 0, 255); err != nil {
			t.Fatal(err)
		}
		if got[off] != 0x04 || got[off+1] != 0x00 || got[off+2] != 0xff || got[off+3] != 0xff {
			t.Errorf("%s: blue not in slot %d: % x", name, slot, got[off:off+4])
		}
		want := append([]byte(nil), fx["base"]...)
		_ = lsApplyEffect(want, name, false, 0, 0, 0)
		for i := range got {
			if (i < off || i >= off+4) && got[i] != want[i] {
				t.Errorf("%s: byte %d changed outside slot %d", name, i, slot)
			}
		}
		if string(fx[name][off:off+4]) == "\x00\x00\xff\xff" { // red in the capture
			red := append([]byte(nil), fx["base"]...)
			_ = lsApplyEffect(red, name, true, 255, 0, 0)
			lsFixCRC(red)
			if string(red) != string(fx[name]) {
				t.Errorf("%s: red base colour doesn't reproduce the capture", name)
			}
		}
	}
}

// Shared effects use the D5 Next's selector values; every mode advertised has a block.
func TestLeakshieldEffectSelectors(t *testing.T) {
	for name, h := range lsEffectBlocks {
		b, _ := hex.DecodeString(h[:2])
		if v, ok := Effects[name]; ok && v != b[0] {
			t.Errorf("%s: selector %02x, D5 Next uses %02x", name, b[0], v)
		}
		if leakshieldEffectName(b[0]) != name {
			t.Errorf("%s: reverse lookup failed", name)
		}
	}
	for _, m := range LeakshieldLightingModes() {
		if _, ok := lsEffectBlocks[m.Name]; !ok && m.Name != "off" && m.Name != "status" {
			t.Errorf("mode %s has no effect block", m.Name)
		}
	}
}
