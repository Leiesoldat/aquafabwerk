// Package aqua controls the RGB of Aquacomputer devices (D5 Next pump, and later the
// High Flow Next) over USB HID, by read-modify-write of their config feature report.
//
// Protocol reverse-engineered 2026-08 (see ~/src/aquapx-re/PROTOCOL.md): the LED settings
// live inside the device's big config report (D5 Next: report 0x03, 809 bytes) which also
// holds pump/fan config, so writes are always read-modify-write touching ONLY the LED fields,
// with a fresh CRC-16/USB. A read's CRC is self-checked first — that both guards against a
// bad read and confirms our field offsets match this unit's framing before we ever write.
package aqua

import (
	"fmt"
	"math"
	"sync"

	ghid "github.com/sstallion/go-hid"
)

const (
	d5nVID = 0x0c70
	d5nPID = 0xf00e

	d5nReport = 0x03
	d5nLen    = 809 // report id + 808-byte body

	// Field offsets within the 809-byte report 0x03 (see PROTOCOL.md).
	offBright = 237 // master brightness 0..255
	offOnOff  = 239 // 0x00 = on, 0x02 = off
	offEffect = 663 // effect selector (see Effects)
	offHue    = 706 // hue uint16 big-endian, scale 1536/360deg
	offSat    = 708 // saturation 0..255
	offCRC    = 807 // CRC-16/USB over [1:807], big-endian
)

// hueFull is the device's hue value for a full 360-degree colour wheel.
const hueFull = 1536

// saveReport is the constant 11-byte "commit" feature report (0x02) aquasuite sends after a
// config write to persist it; identical across all captures, so replayed verbatim.
var saveReport = []byte{0x02, 0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00, 0x00, 0x34, 0xc6}

// Effects maps a friendly name to the D5 Next effect selector byte (offset 663), decoded
// from the OpenRGB #2111 captures.
var Effects = map[string]byte{
	"static":         0x01,
	"breathing":      0x02,
	"rainbow":        0x03,
	"blinking":       0x04,
	"colorchange":    0x05,
	"sequence":       0x07,
	"scanner":        0x08,
	"laser":          0x09,
	"wave":           0x0a,
	"colorsequence":  0x0b,
	"colorshift":     0x0c,
	"flame":          0x0e,
	"rain":           0x0f,
	"swipingrainbow": 0x13,
}

// D5Next is an open connection to the pump's HID interface. Set NoSave to skip the
// save/commit report — the colour still applies live, but isn't persisted to the device's
// EEPROM. Use it for frequently-updated things (the status daemon) to avoid flash wear.
type D5Next struct {
	dev    *ghid.Device
	NoSave bool
	// mu serialises whole read-modify-write cycles. hidapi handles are NOT safe for concurrent
	// feature-report access — overlapping GET_FEATUREs on one handle make the device EPIPE
	// ("Broken pipe") and wedge until a USB reset.
	mu sync.Mutex
}

// OpenD5Next opens the D5 Next's HID interface. Uses Enumerate+OpenPath rather than
// Open(vid,pid,"") — go-hid treats an empty-string serial as a filter (not NULL), so the
// vararg Open fails to match. The pump exposes one HID interface (its vendor interface isn't
// a HID device), so the first enumerated path is the right one.
func OpenD5Next() (*D5Next, error) {
	if err := ghid.Init(); err != nil {
		return nil, fmt.Errorf("hid init: %w", err)
	}
	var path string
	err := ghid.Enumerate(d5nVID, d5nPID, func(info *ghid.DeviceInfo) error {
		if path == "" {
			path = info.Path
		}
		return nil
	})
	if err != nil {
		_ = ghid.Exit()
		return nil, fmt.Errorf("enumerate %04x:%04x: %w", d5nVID, d5nPID, err)
	}
	if path == "" {
		_ = ghid.Exit()
		return nil, fmt.Errorf("D5 Next %04x:%04x not found", d5nVID, d5nPID)
	}
	dev, err := ghid.OpenPath(path)
	if err != nil {
		_ = ghid.Exit()
		return nil, fmt.Errorf("open D5 Next: %w", err)
	}
	return &D5Next{dev: dev}, nil
}

// Close releases the device.
func (d *D5Next) Close() error {
	if d.dev != nil {
		_ = d.dev.Close()
	}
	return ghid.Exit()
}

// ReadConfig returns the live 809-byte config report, after verifying its CRC self-checks
// (which also confirms our field offsets line up with this unit's report framing).
func (d *D5Next) readConfig() ([]byte, error) {
	buf := make([]byte, d5nLen)
	buf[0] = d5nReport
	n, err := d.dev.GetFeatureReport(buf)
	if err != nil {
		return nil, fmt.Errorf("get report 0x03: %w", err)
	}
	if n != d5nLen {
		return nil, fmt.Errorf("unexpected config length %d (want %d)", n, d5nLen)
	}
	calc := CRC16USB(buf[1:offCRC])
	stored := uint16(buf[offCRC])<<8 | uint16(buf[offCRC+1])
	if calc != stored {
		return nil, fmt.Errorf("config CRC self-check failed (calc=%04x stored=%04x): framing/offsets not confirmed, refusing", calc, stored)
	}
	return buf, nil
}

// WriteConfig recomputes the CRC over the (already-modified) buffer and writes it back,
// then sends the save/commit report. cfg must be a full 809-byte report 0x03 buffer.
func (d *D5Next) writeConfig(cfg []byte) error {
	if len(cfg) != d5nLen {
		return fmt.Errorf("bad config length %d", len(cfg))
	}
	c := CRC16USB(cfg[1:offCRC])
	cfg[offCRC] = byte(c >> 8)
	cfg[offCRC+1] = byte(c)
	if _, err := d.dev.SendFeatureReport(cfg); err != nil {
		return fmt.Errorf("set report 0x03: %w", err)
	}
	if d.NoSave {
		return nil
	}
	if _, err := d.dev.SendFeatureReport(saveReport); err != nil {
		return fmt.Errorf("save report 0x02: %w", err)
	}
	return nil
}

// SetColor sets a solid colour (static effect, on) from 8-bit RGB.
func (d *D5Next) SetColor(r, g, b byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	cfg, err := d.readConfig()
	if err != nil {
		return err
	}
	applyColor(cfg, r, g, b)
	cfg[offEffect] = Effects["static"]
	cfg[offOnOff] = 0x00
	return d.writeConfig(cfg)
}

// SetEffect sets a named onboard effect with a base colour (ignored by effects that don't
// use one, e.g. rainbow). Returns an error for an unknown effect name.
func (d *D5Next) SetEffect(name string, r, g, b byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	eff, ok := Effects[name]
	if !ok {
		return fmt.Errorf("unknown effect %q", name)
	}
	cfg, err := d.readConfig()
	if err != nil {
		return err
	}
	applyColor(cfg, r, g, b)
	cfg[offEffect] = eff
	cfg[offOnOff] = 0x00
	return d.writeConfig(cfg)
}

// Off turns the LEDs off (keeps the effect/colour, sets the off flag).
func (d *D5Next) Off() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	cfg, err := d.readConfig()
	if err != nil {
		return err
	}
	cfg[offOnOff] = 0x02
	return d.writeConfig(cfg)
}

// State is a human-readable summary of the current LED config.
type State struct {
	Brightness byte
	On         bool
	Effect     byte
	EffectName string
	Hue        uint16
	Saturation byte
}

// Read returns the current LED state (for a dump/status command).
func (d *D5Next) Read() (*State, []byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cfg, err := d.readConfig()
	if err != nil {
		return nil, nil, err
	}
	s := &State{
		Brightness: cfg[offBright],
		On:         cfg[offOnOff] == 0x00,
		Effect:     cfg[offEffect],
		Hue:        uint16(cfg[offHue])<<8 | uint16(cfg[offHue+1]),
		Saturation: cfg[offSat],
	}
	for name, v := range Effects {
		if v == s.Effect {
			s.EffectName = name
		}
	}
	return s, cfg, nil
}

// applyColor writes hue/sat/brightness fields for an RGB colour into a config buffer.
func applyColor(cfg []byte, r, g, b byte) {
	h, s, v := rgbToHSV(r, g, b)
	hue := uint16(math.Round(h/360*hueFull)) % hueFull
	cfg[offHue] = byte(hue >> 8)
	cfg[offHue+1] = byte(hue)
	cfg[offSat] = byte(math.Round(s * 255))
	cfg[offBright] = byte(math.Round(v * 255))
}

// rgbToHSV converts 8-bit RGB to H[0,360) S[0,1] V[0,1].
func rgbToHSV(r, g, b byte) (float64, float64, float64) {
	rf, gf, bf := float64(r)/255, float64(g)/255, float64(b)/255
	max := math.Max(rf, math.Max(gf, bf))
	min := math.Min(rf, math.Min(gf, bf))
	v := max
	delta := max - min
	var s float64
	if max > 0 {
		s = delta / max
	}
	var h float64
	if delta != 0 {
		switch max {
		case rf:
			h = math.Mod((gf-bf)/delta, 6)
		case gf:
			h = (bf-rf)/delta + 2
		default:
			h = (rf-gf)/delta + 4
		}
		h *= 60
		if h < 0 {
			h += 360
		}
	}
	return h, s, v
}
