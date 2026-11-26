package aqua

// LEAKSHIELD: same config-report protocol as the D5 Next (report 0x03, CRC-16/USB over
// [1:len-2] big-endian in the last two bytes, then the constant 11-byte save report 0x02), with
// its own 750-byte layout. The RGB is a single HSV record [hue uint16 BE][saturation][value] at 620.
//
// Decoded 2026-11-25 from aquasuite USBPcap captures (Windows, aquasuite -> LEAKSHIELD) by
// diffing SET_REPORT writes that changed one setting at a time (red/blue/green hue, brightness,
// pastel saturation): only those four bytes + the CRC ever changed. Every captured write was
// accepted and every CRC validated, and the save report was byte-identical to the D5 Next's.
// The captures show aquasuite sends that save report as an OUTPUT report (SET_REPORT wValue
// 0x0202), not a feature report, so writeConfig replays it with Write (hidraw write; IF1 has no
// interrupt-OUT endpoint, so the kernel issues it as a control SET_REPORT(Output)).
//
// NOT yet confirmed: that a live GET_FEATURE(0x03) returns this same 750-byte layout (the
// captures only contain writes). readConfig's length + CRC self-check gates every write on
// that, and Probe/ProbeConfig exist to capture the raw read for decoding if it doesn't match.

import (
	"fmt"
	"math"
	"sync"

	ghid "github.com/sstallion/go-hid"
)

const (
	lsVID = 0x0c70
	lsPID = 0xf014

	// lsInterface is the HID interface carrying the config feature reports. Unlike the D5 Next
	// (one HID interface), the LEAKSHIELD exposes TWO HID interfaces (1 and 2, plus a vendor
	// interface 0); the captured SET_REPORTs all target wIndex=1.
	lsInterface = 1

	lsReport = 0x03
	lsLen    = 750 // report id + 749-byte body (wLength 0x02ee in the captures)

	// Field offsets within the 750-byte report 0x03 (see re/PROTOCOL.md).
	lsOffHue    = 620 // hue uint16 big-endian, scale 1536/360deg (red 0x0000, green 0x01ff, blue 0x03ff)
	lsOffSat    = 622 // saturation 0..255 (0xff full colour, 0x7f pastel)
	lsOffBright = 623 // brightness/value 0..255
	lsOffCRC    = 748 // CRC-16/USB over [1:748], big-endian

	// lsProbeLen is the buffer used by Probe: larger than lsLen so a longer-than-expected
	// report is visible (n > lsLen) instead of being silently truncated.
	lsProbeLen = 1024
)

// Leakshield is an open connection to the LEAKSHIELD's config HID interface. NoSave skips the
// save/commit report (applies live without persisting) — see D5Next.NoSave.
type Leakshield struct {
	dev    *ghid.Device
	NoSave bool
	// Path and Interface identify the opened hidraw node (for probe diagnostics).
	Path      string
	Interface int
	// mu serialises read-modify-write cycles — see D5Next.mu (concurrent feature reports on
	// one hidapi handle EPIPE and wedge the device).
	mu sync.Mutex
}

// OpenLeakshield opens the LEAKSHIELD's config interface (Enumerate+OpenPath — see OpenD5Next).
// It picks the HID interface numbered lsInterface; if the platform doesn't report interface
// numbers (InterfaceNbr == -1), it falls back to the first enumerated path.
func OpenLeakshield() (*Leakshield, error) {
	if err := ghid.Init(); err != nil {
		return nil, fmt.Errorf("hid init: %w", err)
	}
	var path, first string
	iface, firstIface := -1, -1
	err := ghid.Enumerate(lsVID, lsPID, func(info *ghid.DeviceInfo) error {
		if first == "" {
			first, firstIface = info.Path, info.InterfaceNbr
		}
		if path == "" && info.InterfaceNbr == lsInterface {
			path, iface = info.Path, info.InterfaceNbr
		}
		return nil
	})
	if err != nil {
		_ = ghid.Exit()
		return nil, fmt.Errorf("enumerate %04x:%04x: %w", lsVID, lsPID, err)
	}
	if path == "" {
		path, iface = first, firstIface
	}
	if path == "" {
		_ = ghid.Exit()
		return nil, fmt.Errorf("LEAKSHIELD %04x:%04x not found", lsVID, lsPID)
	}
	dev, err := ghid.OpenPath(path)
	if err != nil {
		_ = ghid.Exit()
		return nil, fmt.Errorf("open LEAKSHIELD %s (interface %d): %w (permissions? try sudo or the udev rule)", path, iface, err)
	}
	return &Leakshield{dev: dev, Path: path, Interface: iface}, nil
}

// Close releases the device.
func (l *Leakshield) Close() error {
	if l.dev != nil {
		_ = l.dev.Close()
	}
	return ghid.Exit()
}

// ProbeResult is the raw, unvalidated result of a GET_FEATURE on the config report, for
// confirming (or decoding) the live layout before trusting it for writes.
type ProbeResult struct {
	Data     []byte // bytes actually returned (len == N)
	N        int    // byte count hidapi reported
	LenOK    bool   // N == lsLen
	CRCOK    bool   // CRC-16/USB over [1:N-2] matches the last two bytes (big-endian)
	CRCCalc  uint16
	CRCStore uint16
}

// Probe reads feature report rid and reports length + CRC status WITHOUT refusing on mismatch.
// Read-only: it never writes to the device. It first asks with an oversized buffer (so a longer
// report is visible); some firmware stalls a GET_REPORT whose wLength exceeds the report size,
// so on error it retries with the exact captured length (750 for report 0x03).
func (l *Leakshield) Probe(rid byte) (*ProbeResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	buf := make([]byte, lsProbeLen)
	buf[0] = rid
	n, err := l.dev.GetFeatureReport(buf)
	if err != nil && rid == lsReport {
		first := err
		buf = make([]byte, lsLen)
		buf[0] = rid
		n, err = l.dev.GetFeatureReport(buf)
		if err != nil {
			return nil, fmt.Errorf("get report 0x%02x: %v (with %d-byte buffer), %w (with %d-byte buffer)", rid, first, lsProbeLen, err, lsLen)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("get report 0x%02x: %w", rid, err)
	}
	if n < 0 || n > len(buf) {
		return nil, fmt.Errorf("get report 0x%02x: bad length %d", rid, n)
	}
	p := &ProbeResult{Data: append([]byte(nil), buf[:n]...), N: n, LenOK: n == lsLen}
	if n >= 4 {
		p.CRCCalc = CRC16USB(buf[1 : n-2])
		p.CRCStore = uint16(buf[n-2])<<8 | uint16(buf[n-1])
		p.CRCOK = p.CRCCalc == p.CRCStore
	}
	return p, nil
}

// readConfig returns the live 750-byte config report, after verifying its length and CRC
// self-check (which also confirms the capture-derived offsets line up with the live read).
func (l *Leakshield) readConfig() ([]byte, error) {
	buf := make([]byte, lsLen)
	buf[0] = lsReport
	n, err := l.dev.GetFeatureReport(buf)
	if err != nil {
		return nil, fmt.Errorf("get report 0x03: %w", err)
	}
	if n != lsLen {
		return nil, fmt.Errorf("unexpected config length %d (want %d): live layout not confirmed, refusing (run 'aquapx ls probe')", n, lsLen)
	}
	calc := CRC16USB(buf[1:lsOffCRC])
	stored := uint16(buf[lsOffCRC])<<8 | uint16(buf[lsOffCRC+1])
	if calc != stored {
		return nil, fmt.Errorf("config CRC self-check failed (calc=%04x stored=%04x): framing/offsets not confirmed, refusing (run 'aquapx ls probe')", calc, stored)
	}
	return buf, nil
}

// writeConfig recomputes the CRC over the (already-modified) buffer and writes it back,
// then sends the save/commit report. cfg must be a full 750-byte report 0x03 buffer.
func (l *Leakshield) writeConfig(cfg []byte) error {
	if len(cfg) != lsLen {
		return fmt.Errorf("bad config length %d", len(cfg))
	}
	c := CRC16USB(cfg[1:lsOffCRC])
	cfg[lsOffCRC] = byte(c >> 8)
	cfg[lsOffCRC+1] = byte(c)
	if _, err := l.dev.SendFeatureReport(cfg); err != nil {
		return fmt.Errorf("set report 0x03: %w", err)
	}
	if l.NoSave {
		return nil
	}
	if _, err := l.dev.Write(saveReport); err != nil {
		return fmt.Errorf("save report 0x02 (output): %w", err)
	}
	return nil
}

// SetColor sets the LED colour from 8-bit RGB (hue, saturation and brightness).
func (l *Leakshield) SetColor(r, g, b byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	cfg, err := l.readConfig()
	if err != nil {
		return err
	}
	lsApplyColor(cfg, r, g, b)
	return l.writeConfig(cfg)
}

// Off sets brightness to 0, keeping hue and saturation. (The LEAKSHIELD's on/off flag, if it
// has one, isn't decoded; zero brightness is the captured way to darken the LEDs.)
func (l *Leakshield) Off() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	cfg, err := l.readConfig()
	if err != nil {
		return err
	}
	cfg[lsOffBright] = 0
	return l.writeConfig(cfg)
}

// LeakshieldState is a human-readable summary of the current LED config.
type LeakshieldState struct {
	Hue        uint16
	HueDeg     float64
	Saturation byte
	Brightness byte
}

// Read returns the current LED state and the raw (validated) config.
func (l *Leakshield) Read() (*LeakshieldState, []byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cfg, err := l.readConfig()
	if err != nil {
		return nil, nil, err
	}
	hue := uint16(cfg[lsOffHue])<<8 | uint16(cfg[lsOffHue+1])
	s := &LeakshieldState{
		Hue:        hue,
		HueDeg:     float64(hue) / hueFull * 360,
		Saturation: cfg[lsOffSat],
		Brightness: cfg[lsOffBright],
	}
	return s, cfg, nil
}

// ApplyLighting applies a CoolerControl lighting selection to the LEAKSHIELD. Only a static
// colour is decoded (no effect selector yet), so any mode other than "off" sets the colour.
func (l *Leakshield) ApplyLighting(mode string, r, g, b byte) error {
	if mode == "off" {
		return l.Off()
	}
	return l.SetColor(r, g, b)
}

// lsApplyColor writes hue/sat/brightness for an RGB colour into a LEAKSHIELD config buffer.
func lsApplyColor(cfg []byte, r, g, b byte) {
	h, s, v := rgbToHSV(r, g, b)
	hue := uint16(math.Round(h/360*hueFull)) % hueFull
	cfg[lsOffHue] = byte(hue >> 8)
	cfg[lsOffHue+1] = byte(hue)
	cfg[lsOffSat] = byte(math.Round(s * 255))
	cfg[lsOffBright] = byte(math.Round(v * 255))
}
