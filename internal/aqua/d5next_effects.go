package aqua

// D5 Next onboard effects, decoded 2026-11-27 from an aquasuite USB capture that stepped
// through all 23 effects in the D5 Next's list (testdata/d5next_effects.txt).
//
// The D5 Next's per-effect block is bytes [663:730) of report 0x03 and has exactly the same
// 67-byte layout as the LEAKSHIELD's [577:644): selector, sub-parameter, speed/timing params,
// then 4-byte HSV colour slots [hue16 BE][sat][val] from block offset 43 (= 706, the pump's
// hue field). The 16 effects both devices offer are byte-identical in the captures, so the
// D5 Next reuses lsEffectBlocks for those and adds its own below. Like the LEAKSHIELD,
// selecting an effect copies aquasuite's whole block, then puts the chosen colour into the
// effect's primary slot; writing only the selector byte (what aquapx did before) leaves the
// previous effect's sub-parameter and palette behind.

import (
	"encoding/hex"
	"fmt"
	"math"
	"sort"
)

const (
	d5OffBlockEnd = 730 // end (exclusive) of the per-effect block [663:730)
	d5SlotBase    = 43  // first colour slot, relative to the block start (= offHue)
)

// d5OnlyEffectBlocks are the D5 Next effects the LEAKSHIELD doesn't have, as captured.
var d5OnlyEffectBlocks = map[string]string{
	"rain":        "0f0000ffff0a0f0000006401640000006400ff002800040019001e0000000000000000000000000000000000000001007800ff00000000000000000000000000000000",
	"snowfall":    "100010ffff0a0f0000006401640000006400ff00320003000a000f0000000000000000000000000000000000000001007800ff00000000000000000000000000000000",
	"stardust":    "110000ffff0a0f0000006401640000006400ff00190003003c000f0000000000000000000000000000000000000001007800ff00000000000000000000000000000000",
	"soundbars":   "150006001401010000006400ff0000006400ff000000640000000100190032004b0005000000000000000000000005000000ff0000ffff00ffffff0200ffff0500ffff",
	"soundflash":  "140000ffff0a0f0000006400ff0000006400ff0000000000000000000000000000000000000000000000000080ff140400ffff0000ffff0200ffff0500ffff00000000",
	"soundslider": "160000ffff0a0f0000006400ff0000006400ff00000000000000000005000500050005000a0000000000000000ff320400ffff0000ffff0200ffff0500ffff0000ffff",
	"soundshift":  "170006ffff0a0f0000006400ff0000006400ff000a00020002000a003200000000000000000000000000000100ff320400ffff0000ffff000000000000000000000000",
	"ambientpx":   "180000ffff0a0f0000006400ff0000006400ff0000000000000000000000000000000000000000000000000100ff1e0000000000000000000000000000000000000000",
}

// d5SharedEffects are the LEAKSHIELD effects the D5 Next also has (everything except
// Colour Gradient, which is LEAKSHIELD-only).
var d5SharedEffects = []string{
	"rainbow", "swipingrainbow", "static", "breathing", "colorshift", "colorchange",
	"blinking", "colorsequence", "sequence", "scanner", "laser", "wave", "flame",
	"colorswitch", "bargraph",
}

// d5HostDriven are effects aquasuite animates from data it streams from the PC (audio levels,
// screen content). The pump accepts the selector, but without that stream they don't animate,
// so they're CLI-only and not offered to CoolerControl.
var d5HostDriven = map[string]bool{
	"soundbars": true, "soundflash": true, "soundslider": true, "soundshift": true, "ambientpx": true,
}

// d5PrimarySlot: the colour slot (0-based) that takes the base colour. Rain, Snowfall and
// Stardust have a dim background in slot 0 and the drop/flake colour in slot 1. Effects not
// listed are self-coloured (rainbow, flame, palettes, sound/ambient).
var d5PrimarySlot = map[string]int{
	"rain": 1, "snowfall": 1, "stardust": 1,
}

// d5EffectBlock returns the captured block for a D5 Next effect.
func d5EffectBlock(name string) (string, bool) {
	if h, ok := d5OnlyEffectBlocks[name]; ok {
		return h, true
	}
	for _, n := range d5SharedEffects {
		if n == name {
			return lsEffectBlocks[name], true
		}
	}
	return "", false
}

// d5Primary returns the primary colour slot for an effect (ok=false if self-coloured).
func d5Primary(name string) (int, bool) {
	if s, ok := d5PrimarySlot[name]; ok {
		return s, true
	}
	if _, shared := lsEffectBlocks[name]; shared {
		s, ok := lsPrimarySlot[name]
		return s, ok
	}
	return 0, false
}

// D5NextEffects returns the names of all D5 Next onboard effects, sorted.
func D5NextEffects() []string {
	names := append([]string(nil), d5SharedEffects...)
	for n := range d5OnlyEffectBlocks {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// d5EffectName returns the effect name for selector byte v ("" if unknown).
func d5EffectName(v byte) string {
	for _, n := range D5NextEffects() {
		if h, _ := d5EffectBlock(n); h != "" {
			if b, err := hex.DecodeString(h[:2]); err == nil && b[0] == v {
				return n
			}
		}
	}
	return ""
}

// d5ApplyEffect writes effect name's captured block into a D5 Next config buffer and turns the
// LEDs on. With withColor, effects that take a base colour get r,g,b's hue and saturation in
// their primary slot and its brightness as the master brightness (as SetColor always did);
// self-coloured effects get full master brightness, as in every aquasuite capture, since
// CoolerControl sends them no colour (black would otherwise blank the ring).
func d5ApplyEffect(cfg []byte, name string, withColor bool, r, g, b byte) error {
	h, ok := d5EffectBlock(name)
	if !ok {
		return fmt.Errorf("unknown D5 Next effect %q (have %v)", name, D5NextEffects())
	}
	blk, err := hex.DecodeString(h)
	if err != nil || len(blk) != d5OffBlockEnd-offEffect {
		return fmt.Errorf("bad effect block for %q", name)
	}
	copy(cfg[offEffect:d5OffBlockEnd], blk)
	cfg[offOnOff] = 0x00
	if !withColor {
		return nil
	}
	if slot, ok := d5Primary(name); ok {
		hh, s, v := rgbToHSV(r, g, b)
		hue := uint16(math.Round(hh/360*hueFull)) % hueFull
		off := offEffect + d5SlotBase + 4*slot
		cfg[off] = byte(hue >> 8)
		cfg[off+1] = byte(hue)
		cfg[off+2] = byte(math.Round(s * 255))
		cfg[offBright] = byte(math.Round(v * 255))
	} else {
		cfg[offBright] = 0xff
	}
	return nil
}
