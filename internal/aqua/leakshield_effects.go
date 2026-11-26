package aqua

// LEAKSHIELD onboard effects, decoded 2026-11-26 from an aquasuite USB capture that stepped
// through every effect in the LEAKSHIELD's list (re/decode_leakshield.py).
//
// The effect selector is byte 577 and uses the SAME values as the D5 Next (rainbow 0x03,
// static 0x01, ...), plus three LEAKSHIELD-only effects: colour switch 0x12, bar graph 0x0d and
// colour gradient 0x21. Each effect also carries its own parameter block (speed, timing etc.,
// 579-619) and colour slots (4-byte HSV records [hue16 BE][sat][val] from 620), and only
// [577:644] ever changed between effects. aquasuite rewrites that whole block when switching,
// so selecting an effect copies aquasuite's exact block (lsEffectBlocks), then puts the chosen
// colour into that effect's primary colour slot.

import (
	"encoding/hex"
	"fmt"
	"sort"
)

const (
	lsOffEffect   = 577 // effect selector (same values as the D5 Next's Effects)
	lsOffBlockEnd = 644 // end (exclusive) of the per-effect block [577:644]
)

// lsEffectBlocks holds aquasuite's bytes [577:644] for each effect, as captured.
var lsEffectBlocks = map[string]string{
	"rainbow":        "030000ffff0a0f0000006401640000006400ff0032006400000000000000000000000000000000000000000000ffff0000000000000000000000000000000000000000",
	"swipingrainbow": "130000ffff0a0f0000006401640000006400ff0023000a0005005a00190000000000000000000000000000000000ff0100ffff00000000000000000000000000000000",
	"static":         "010000ffff0a0f0000006400ff0000006400ff0000000000000000000000000000000000000000000000000000ffff0000000000000000000000000000000000000000",
	"breathing":      "020000ffff0a0f000000640164000000640164003c005000050014000000000000000000000000000000000080ffff0000000000000000000000000000000000000000",
	"colorshift":     "0c0000ffff0a0f0000006401640000006400ff001e003200190000000000000000000000000000000000000100ffff0000000000000000000000000000000000000000",
	"colorchange":    "050004ffff0a0f0000006401640000006400ff0028000200000000000000000000000000000000000000000000ffff0100ffff0200ffff000000000000000000000000",
	"blinking":       "040006ffff0a0f0000006401640000006400ff0028000100000000000000000000000000000000000000000000000f0000ffff00000000000000000000000000000000",
	"colorsequence":  "0b0000ffff0a0f0000006400640000006400ff001e002800010002005000000000000000000000000000000000ffff0100ffff00000000000000000000000000000000",
	"sequence":       "070000ffff0a0f0000006401640000006400ff0019001e0002000a000500000000000000000000000000000000000f0000ffff0400ffff000000000000000000000000",
	"scanner":        "080000ffff0a0f0000006400640000006400ff0019002800140000000000000000000000000000000000000000000f0000ffff0200ffff000000000000000000000000",
	"laser":          "090000ffff0a0f0000006400640000006400ff0019002800140000000000000000000000000000000000000000000f0200ffff0400ffff000000000000000000000000",
	"wave":           "0a0000ffff0a0f0000006400640000006400ff0019002800140001000000000000000000000000000000000000000f0000ffff00000000000000000000000000000000",
	"flame":          "0e0000ffff0a0f0000006401640000006400ff00320000000000000000000000000000000000000000000000c8c81900c8ff960078ffff000000000000000000000000",
	"colorswitch":    "120000ffff0a0f0000006400ff0000006400ff0001000000190032006400640064006400000000000000000000ffff00ffffff0200ffff0300ffff0400ffff0500ffff",
	"bargraph":       "0d0006ffff0a0f0000006400640000006400ff00000064000000010032006400c80000000000000000000000000005000000ff0000ffff00ffffff0200ffff0500ffff",
	"colorgradient":  "210007ffff0a0f0000006400640000006400ff000003e80000000300fa01f402ee0000000000000000000000000000000000050000ffff0200ffff0400ffff008000ff",
}

// lsPrimarySlot is the colour slot (0-based, slot k at 620+4k) that takes CoolerControl's base
// colour for single-colour effects: the slot aquasuite filled with the user's chosen colour (or
// first foreground slot after a dim background record). Effects absent here are self-coloured
// (rainbow, flame, multi-colour palettes) and ignore the base colour.
var lsPrimarySlot = map[string]int{
	"static":        0,
	"breathing":     0,
	"colorshift":    0,
	"colorchange":   0,
	"colorsequence": 0,
	"blinking":      1,
	"sequence":      1,
	"scanner":       1,
	"laser":         1,
	"wave":          1,
}

// LeakshieldEffects returns the names of the LEAKSHIELD's onboard effects, sorted.
func LeakshieldEffects() []string {
	names := make([]string, 0, len(lsEffectBlocks))
	for n := range lsEffectBlocks {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// LeakshieldLightingModes is the LEAKSHIELD's CoolerControl mode catalogue, in aquasuite's order.
func LeakshieldLightingModes() []LightingMode {
	return []LightingMode{
		{"status", "System Status (match fans)", 0, 0},
		{"off", "Off", 0, 0},
		{"rainbow", "Rotating Rainbow", 0, 0},
		{"swipingrainbow", "Swiping Rainbow", 0, 0},
		{"static", "Single Colour", 1, 1},
		{"breathing", "Breathing", 1, 1},
		{"colorshift", "Colour Shift", 1, 1},
		{"colorchange", "Colour Change", 1, 1},
		{"blinking", "Blinking", 1, 1},
		{"colorsequence", "Colour Sequence", 1, 1},
		{"sequence", "Sequence", 1, 1},
		{"scanner", "Scanner", 1, 1},
		{"laser", "Laser", 1, 1},
		{"wave", "Wave", 1, 1},
		{"flame", "Flame", 0, 0},
		{"colorswitch", "Colour Switch", 0, 0},
		{"bargraph", "Bar Graph", 0, 0},
		{"colorgradient", "Colour Gradient", 0, 0},
	}
}

// lsApplyEffect writes effect name's captured block into cfg and, for single-colour effects,
// the RGB colour into its primary slot. withColor=false leaves aquasuite's captured colours.
func lsApplyEffect(cfg []byte, name string, withColor bool, r, g, b byte) error {
	h, ok := lsEffectBlocks[name]
	if !ok {
		return fmt.Errorf("unknown LEAKSHIELD effect %q (have %v)", name, LeakshieldEffects())
	}
	blk, err := hex.DecodeString(h)
	if err != nil || len(blk) != lsOffBlockEnd-lsOffEffect {
		return fmt.Errorf("bad effect block for %q", name)
	}
	copy(cfg[lsOffEffect:lsOffBlockEnd], blk)
	if slot, ok := lsPrimarySlot[name]; ok && withColor {
		lsApplyColorAt(cfg, lsOffHue+4*slot, r, g, b)
	}
	return nil
}

// SetEffect switches the LEAKSHIELD to an onboard effect, using r,g,b as its base colour where
// the effect takes one.
func (l *Leakshield) SetEffect(name string, r, g, b byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	cfg, err := l.readConfig()
	if err != nil {
		return err
	}
	if err := lsApplyEffect(cfg, name, true, r, g, b); err != nil {
		return err
	}
	return l.writeConfig(cfg)
}

// leakshieldEffectName returns the effect name for selector byte v ("" if unknown).
func leakshieldEffectName(v byte) string {
	for n, h := range lsEffectBlocks {
		if b, err := hex.DecodeString(h[:2]); err == nil && b[0] == v {
			return n
		}
	}
	return ""
}
