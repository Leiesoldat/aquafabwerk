package aqua

import (
	"fmt"
	"math"
	"sync"

	ghid "github.com/sstallion/go-hid"
)

// High Flow Next: same config-report protocol as the D5 Next (report 0x03, CRC-16/USB over
// [1:len-2] big-endian in the last two bytes), but a different, larger layout: 682 bytes made
// of eight ~70-byte LED-group blocks. Each block carries a palette of colour slots at a 4-byte
// stride, each slot being [hue uint16 BE][0xffff marker]. Setting every slot to one hue turns
// the whole indicator that colour while leaving its onboard effect (the rotating flow
// indicator, whose speed tracks flow rate) running.
//
// Reverse-engineered live on hardware 2026-08-11 (no captures exist for this device — the
// OpenRGB #2111 set is D5 Next only), by forcing candidate hue-range values to 0 and observing
// the indicator turn red. Sensors (flow/temp/quality) were verified unaffected by the write.
const (
	hfnVID = 0x0c70
	hfnPID = 0xf012

	hfnReport = 0x03
	hfnLen    = 682
	hfnCRCOff = hfnLen - 2 // 680
)

// hfnBlocks are the LED-group block start offsets; hfnColorLo/Hi bound the palette window
// within each block.
var hfnBlocks = []int{98, 168, 238, 308, 378, 448, 518, 588}

const (
	hfnColorLo = 40
	hfnColorHi = 62
)

// HighFlowNext is an open connection to the flow meter's HID interface. NoSave skips the
// save/commit report (applies live without persisting) — see D5Next.NoSave.
type HighFlowNext struct {
	dev    *ghid.Device
	NoSave bool
	// mu serialises read-modify-write cycles — see D5Next.mu (concurrent feature reports on
	// one hidapi handle EPIPE and wedge the device).
	mu sync.Mutex
}

// OpenHighFlowNext opens the High Flow Next (Enumerate+OpenPath — see OpenD5Next).
func OpenHighFlowNext() (*HighFlowNext, error) {
	if err := ghid.Init(); err != nil {
		return nil, fmt.Errorf("hid init: %w", err)
	}
	var path string
	err := ghid.Enumerate(hfnVID, hfnPID, func(info *ghid.DeviceInfo) error {
		if path == "" {
			path = info.Path
		}
		return nil
	})
	if err != nil {
		_ = ghid.Exit()
		return nil, fmt.Errorf("enumerate %04x:%04x: %w", hfnVID, hfnPID, err)
	}
	if path == "" {
		_ = ghid.Exit()
		return nil, fmt.Errorf("High Flow Next %04x:%04x not found", hfnVID, hfnPID)
	}
	dev, err := ghid.OpenPath(path)
	if err != nil {
		_ = ghid.Exit()
		return nil, fmt.Errorf("open High Flow Next: %w", err)
	}
	return &HighFlowNext{dev: dev}, nil
}

// Close releases the device.
func (f *HighFlowNext) Close() error {
	if f.dev != nil {
		_ = f.dev.Close()
	}
	return ghid.Exit()
}

// ReadConfig returns the live config report, gated on its CRC self-checking (which also
// confirms the layout matches before any write).
func (f *HighFlowNext) readConfig() ([]byte, error) {
	buf := make([]byte, hfnLen)
	buf[0] = hfnReport
	n, err := f.dev.GetFeatureReport(buf)
	if err != nil {
		return nil, fmt.Errorf("get report 0x03: %w", err)
	}
	if n != hfnLen {
		return nil, fmt.Errorf("unexpected config length %d (want %d)", n, hfnLen)
	}
	calc := CRC16USB(buf[1:hfnCRCOff])
	stored := uint16(buf[hfnCRCOff])<<8 | uint16(buf[hfnCRCOff+1])
	if calc != stored {
		return nil, fmt.Errorf("config CRC self-check failed (calc=%04x stored=%04x): refusing to write", calc, stored)
	}
	return buf, nil
}

// WriteConfig recomputes the CRC and writes the config back, then commits it.
func (f *HighFlowNext) writeConfig(cfg []byte) error {
	if len(cfg) != hfnLen {
		return fmt.Errorf("bad config length %d", len(cfg))
	}
	c := CRC16USB(cfg[1:hfnCRCOff])
	cfg[hfnCRCOff] = byte(c >> 8)
	cfg[hfnCRCOff+1] = byte(c)
	if _, err := f.dev.SendFeatureReport(cfg); err != nil {
		return fmt.Errorf("set report 0x03: %w", err)
	}
	if f.NoSave {
		return nil
	}
	if _, err := f.dev.SendFeatureReport(saveReport); err != nil {
		return fmt.Errorf("save report 0x02: %w", err)
	}
	return nil
}

// colorEntries returns the offset of every colour record in every block. A record is FOUR
// bytes — [hue uint16 BE][saturation][value] — laid out at a stride of 4 from hfnColorLo, so a
// block holds six of them. Together they cover both rendered layers (the base/background
// colour and the effect/spinner palette), which is why all of them must be written: setting
// only some gives e.g. a white spinner on a red background.
//
// NOTE: an earlier version walked this window at stride 2 treating any small value as a hue,
// which silently wrote hues into the saturation/value bytes. The record stride is 4.
func colorEntries() []int {
	var out []int
	for _, s := range hfnBlocks {
		for off := hfnColorLo; off+3 < hfnColorHi+4; off += 4 {
			i := s + off
			if i+3 < hfnCRCOff {
				out = append(out, i)
			}
		}
	}
	return out
}

// SetColor writes one colour into every colour record — hue, saturation AND value — so both
// rendered layers match. Saturation is what makes white possible (white is hue-less: sat 0);
// writing hue alone renders hue 0 as RED, not white. The device keeps running its own
// flow-indicator effect (speed tracks flow) in the chosen colour.
func (f *HighFlowNext) SetColor(r, g, b byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg, err := f.readConfig()
	if err != nil {
		return err
	}
	h, s, v := rgbToHSV(r, g, b)
	hue := uint16(math.Round(h/360*hueFull)) % hueFull
	sat := byte(math.Round(s * 255))
	entries := colorEntries()
	if len(entries) == 0 {
		return fmt.Errorf("no colour records found (unexpected layout)")
	}
	for _, i := range entries {
		cfg[i] = byte(hue >> 8)
		cfg[i+1] = byte(hue)
		cfg[i+2] = sat
		// Scale each record's OWN brightness rather than flattening them all to one value: the
		// spinner is rendered by brightness CONTRAST between records (dim background entries vs
		// bright highlight entries). Writing a single val to every record makes the indicator a
		// uniform block and the moving highlight disappears.
		cfg[i+3] = byte(math.Round(float64(cfg[i+3]) * v))
	}
	return f.writeConfig(cfg)
}

// Records returns the current colour records as (hue, sat, val) triples, for dump/status.
func (f *HighFlowNext) Records() ([][3]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg, err := f.readConfig()
	if err != nil {
		return nil, err
	}
	var out [][3]int
	for _, i := range colorEntries() {
		out = append(out, [3]int{int(uint16(cfg[i])<<8 | uint16(cfg[i+1])), int(cfg[i+2]), int(cfg[i+3])})
	}
	return out, nil
}
