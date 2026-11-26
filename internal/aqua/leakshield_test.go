package aqua

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// The fixtures are real aquasuite SET_REPORT(feature 0x03) payloads from the 2026-11-25
// LEAKSHIELD captures (extracted with re/decode_leakshield.py).
func loadLS(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/leakshield_" + name + ".hex")
	if err != nil {
		t.Fatal(err)
	}
	b, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != lsLen {
		t.Fatalf("%s: len %d, want %d", name, len(b), lsLen)
	}
	return b
}

func lsCRCOK(b []byte) bool {
	return CRC16USB(b[1:lsOffCRC]) == uint16(b[lsOffCRC])<<8|uint16(b[lsOffCRC+1])
}

// Every captured write must self-validate with the CRC range/offset the driver uses.
func TestLeakshieldCapturedCRC(t *testing.T) {
	for _, n := range []string{"red", "blue", "green_dim", "pastel_green"} {
		if b := loadLS(t, n); !lsCRCOK(b) || b[0] != lsReport {
			t.Errorf("%s: CRC/report-id check failed", n)
		}
	}
}

// Patching only the colour record of the red capture with another capture's values, then
// recomputing the CRC the way writeConfig does, must reproduce that capture byte-for-byte —
// i.e. the colour lives entirely in [620:624] and nothing else needs to change.
func TestLeakshieldPatchReproducesCaptures(t *testing.T) {
	base := loadLS(t, "red")
	for _, n := range []string{"blue", "green_dim", "pastel_green"} {
		want := loadLS(t, n)
		got := append([]byte(nil), base...)
		copy(got[lsOffHue:lsOffBright+1], want[lsOffHue:lsOffBright+1])
		c := CRC16USB(got[1:lsOffCRC])
		got[lsOffCRC], got[lsOffCRC+1] = byte(c>>8), byte(c)
		if string(got) != string(want) {
			t.Errorf("%s: patched red config != captured write", n)
		}
	}
}

// lsApplyColor must only touch the colour record, and land on the captured values (within the
// 1-step rounding difference between the 1536 scale and aquasuite's 0x01ff/0x03ff).
func TestLeakshieldApplyColor(t *testing.T) {
	base := loadLS(t, "red")
	cases := []struct {
		r, g, b    byte
		hue        uint16
		sat, value byte
	}{
		{0, 0, 255, 0x0400, 255, 255},     // blue 240deg (aquasuite: 0x03ff)
		{0, 255, 0, 0x0200, 255, 255},     // green 120deg (aquasuite: 0x01ff)
		{0, 127, 0, 0x0200, 255, 127},     // half-bright green (captured value 127)
		{128, 255, 128, 0x0200, 127, 255}, // pastel green (captured sat 127)
		{255, 255, 255, 0x0000, 0, 255},   // white = sat 0
	}
	for _, c := range cases {
		cfg := append([]byte(nil), base...)
		lsApplyColor(cfg, c.r, c.g, c.b)
		hue := uint16(cfg[lsOffHue])<<8 | uint16(cfg[lsOffHue+1])
		if hue != c.hue || cfg[lsOffSat] != c.sat || cfg[lsOffBright] != c.value {
			t.Errorf("#%02x%02x%02x: got hue=%04x sat=%d val=%d, want %04x %d %d",
				c.r, c.g, c.b, hue, cfg[lsOffSat], cfg[lsOffBright], c.hue, c.sat, c.value)
		}
		for i := range cfg {
			if (i < lsOffHue || i > lsOffBright) && cfg[i] != base[i] {
				t.Errorf("#%02x%02x%02x: byte %d changed outside the colour record", c.r, c.g, c.b, i)
			}
		}
	}
}
