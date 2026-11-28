package aqua

// Farbwerk (original, 4 RGB outputs, USB 0c70:f00a), decoded 2026-11-27 from aquasuite USB
// captures (testdata/farbwerk_*.txt, re/PROTOCOL.md):
//
//   - Config: HID feature report 0x03, 237 bytes, CRC-16/USB over [1:235] big-endian at 235,
//     then the save report 0x02 as an OUTPUT report (like the LEAKSHIELD), with 0x03 where the
//     D5 Next's has 0x02. Unlike the other devices, the original Farbwerk stalls a read of
//     report 0x03 (aquasuite never reads it), so aquapx keeps the last written config in a
//     file and modifies that (see readConfig).
//   - Four 48-byte blocks, one per RGB output, at 18, 66, 114, 162 (see fwOff* below).
//   - Software sensors: aquasuite sends output report 0x04 once a second with four
//     temperatures (°C x 100). The "Temperature controlled by software sensor" modes follow
//     them; nothing sends them on Linux unless aquapx does (FarbwerkSensors).
//
// The Farbwerk has no plain single-colour mode, so a solid colour is a software-sensor mode
// whose two gradient colours are equal (fwSolidSensor, fed a constant by the plugin).

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	ghid "github.com/sstallion/go-hid"
)

const (
	fwVID = 0x0c70
	fwPID = 0xf00a

	fwReport = 0x03
	fwLen    = 237 // report id + 236-byte body
	fwOffCRC = 235 // CRC-16/USB over [1:235], big-endian

	fwOutputs    = 4
	fwBlockStart = 18
	fwBlockLen   = 48

	// Offsets within an output block.
	fwOffMode     = 0  // low 7 bits: mode (FarbwerkModes); bit 7: fading effect
	fwOffFromTemp = 4  // gradient from, °C x 100, u16 BE
	fwOffToTemp   = 6  // gradient to, °C x 100, u16 BE
	fwOffColor1   = 8  // colour 1 (cold end): hue, sat, val, each u16 BE 0..4095
	fwOffColor2   = 14 // colour 2 (hot end), same format
	fwOffStrip    = 23 // strip type (0 = 12V Green/Red/Blue, 1 = 12V Blue/Red/Green)

	fwFadeBit = 0x80
	fwScale   = 4095 // colour component full scale

	fwSensorReport = 0x04
	fwSensorLen    = 15
	fwSensorUnset  = 0x7fff
	fwSensorSet    = 0x06 // per-sensor "present" byte

	fwReadTries    = 4
	fwRetryDelay   = 60 * time.Millisecond
	fwMaxReadFails = 3
	fwSensorQuiet  = 100 * time.Millisecond // gap after a sensor report before a config read

	// fwSolidSensor is the software sensor (0-based) whose mode carries solid colours.
	fwSolidSensor = 3
)

// fwSaveReport is the constant 11-byte save report, sent as an OUTPUT report after a config
// write (captured, CRC verified).
var fwSaveReport = []byte{0x02, 0x00, 0x00, 0x00, 0x03, 0x00, 0x00, 0x00, 0x00, 0xf4, 0xfb}

// Farbwerk mode values (block byte +0, low 7 bits).
const (
	FwModeSensor1   = 0x00 // physical temperature sensors 1-4: 0x00-0x03
	FwModeSoftware1 = 0x04 // software sensors 1-4: 0x04-0x07
	FwModeRotating  = 0x0a // rotating colour change
)

// Farbwerk is an open connection to the Farbwerk's HID interface.
type Farbwerk struct {
	dev    *ghid.Device
	NoSave bool
	mu     sync.Mutex // serialises feature/output report traffic on the handle

	// ConfigPath overrides FarbwerkConfigPath (tests). Source is where the last config read
	// came from: "device", "saved" or "template".
	ConfigPath string
	Source     string
	readFails  int       // consecutive failed readConfig calls; at fwMaxReadFails the device isn't asked
	lastSensor time.Time // when the last software sensor report was sent
}

// OpenFarbwerk opens the Farbwerk's HID interface (Enumerate+OpenPath, see OpenD5Next).
func OpenFarbwerk() (*Farbwerk, error) {
	if err := ghid.Init(); err != nil {
		return nil, fmt.Errorf("hid init: %w", err)
	}
	var path string
	err := ghid.Enumerate(fwVID, fwPID, func(info *ghid.DeviceInfo) error {
		if path == "" {
			path = info.Path
		}
		return nil
	})
	if err != nil {
		_ = ghid.Exit()
		return nil, fmt.Errorf("enumerate %04x:%04x: %w", fwVID, fwPID, err)
	}
	if path == "" {
		_ = ghid.Exit()
		return nil, fmt.Errorf("Farbwerk %04x:%04x not found", fwVID, fwPID)
	}
	dev, err := ghid.OpenPath(path)
	if err != nil {
		_ = ghid.Exit()
		return nil, fmt.Errorf("open Farbwerk: %w", err)
	}
	return &Farbwerk{dev: dev}, nil
}

// Close releases the device.
func (f *Farbwerk) Close() error {
	if f.dev != nil {
		_ = f.dev.Close()
	}
	return ghid.Exit()
}

// readConfig returns the current config. The original Farbwerk sometimes stalls GET_FEATURE
// 0x03 ("broken pipe"), most likely while it is still handling the once-a-second software
// sensor report, and aquasuite never reads it at all. So a read is retried a few times; if it
// still fails, the copy saved after every write (FarbwerkConfigPath) is used, or the built-in
// template the first time (fwTemplateHex). After fwMaxReadFails failed calls in a row the
// device isn't asked again.
func (f *Farbwerk) readConfig() ([]byte, error) {
	if f.readFails < fwMaxReadFails {
		if d := fwSensorQuiet - time.Since(f.lastSensor); d > 0 {
			time.Sleep(d) // let the Farbwerk finish handling the sensor report first
		}
		var lastErr error
		for try := 0; try < fwReadTries; try++ {
			if try > 0 {
				time.Sleep(fwRetryDelay)
			}
			buf := make([]byte, fwLen)
			buf[0] = fwReport
			n, err := f.dev.GetFeatureReport(buf)
			if err == nil && n == fwLen && fwCRCOK(buf) {
				f.readFails = 0
				f.Source = "device"
				return buf, nil
			}
			if err == nil {
				err = fmt.Errorf("%d bytes / bad CRC", n)
			}
			lastErr = err
		}
		f.readFails++
		log.Printf("farbwerk: config read failed %d times (%v); using the saved copy", fwReadTries, lastErr)
		if f.readFails == fwMaxReadFails {
			log.Printf("farbwerk: config reads keep failing; not asking the device again")
		}
	}
	return fwStoredConfig(f.configPath(), &f.Source)
}

// fwStoredConfig loads the saved config copy, or the template when there is none (or it's
// damaged). src is set to "saved" or "template".
func fwStoredConfig(path string, src *string) ([]byte, error) {
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			if cfg, err := hex.DecodeString(strings.TrimSpace(string(b))); err == nil && len(cfg) == fwLen && fwCRCOK(cfg) {
				*src = "saved"
				return cfg, nil
			}
			log.Printf("farbwerk: saved config %s is damaged; using the template", path)
		}
	}
	cfg, err := hex.DecodeString(fwTemplateHex)
	if err != nil || len(cfg) != fwLen || !fwCRCOK(cfg) {
		return nil, fmt.Errorf("built-in Farbwerk template is invalid")
	}
	*src = "template"
	return cfg, nil
}

func (f *Farbwerk) configPath() string {
	if f.ConfigPath != "" {
		return f.ConfigPath
	}
	return FarbwerkConfigPath()
}

// FarbwerkConfigPath is where the last written Farbwerk config is kept (shared by the plugin
// and the CLI), next to the "All off" levels.
func FarbwerkConfigPath() string {
	return filepath.Join(filepath.Dir(FarbwerkStatePath()), "farbwerk-config.hex")
}

func (f *Farbwerk) writeConfig(cfg []byte) error {
	if len(cfg) != fwLen {
		return fmt.Errorf("bad config length %d", len(cfg))
	}
	fwFixCRC(cfg)
	if _, err := f.dev.SendFeatureReport(cfg); err != nil {
		return fmt.Errorf("set report 0x03: %w", err)
	}
	if p := f.configPath(); p != "" {
		if err := saveFile(p, []byte(hex.EncodeToString(cfg)+"\n")); err != nil {
			log.Printf("farbwerk: couldn't save config copy to %s: %v", p, err)
		}
	}
	if f.NoSave {
		return nil
	}
	if _, err := f.dev.Write(fwSaveReport); err != nil {
		return fmt.Errorf("save report 0x02 (output): %w", err)
	}
	return nil
}

// saveFile writes b to path atomically (temp file + rename).
func saveFile(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func fwCRCOK(b []byte) bool {
	return CRC16USB(b[1:fwOffCRC]) == uint16(b[fwOffCRC])<<8|uint16(b[fwOffCRC+1])
}

func fwFixCRC(b []byte) {
	c := CRC16USB(b[1:fwOffCRC])
	b[fwOffCRC], b[fwOffCRC+1] = byte(c>>8), byte(c)
}

// fwBlock returns the offset of output (1-based) n's block.
func fwBlock(n int) (int, error) {
	if n < 1 || n > fwOutputs {
		return 0, fmt.Errorf("Farbwerk output %d out of range 1-%d", n, fwOutputs)
	}
	return fwBlockStart + (n-1)*fwBlockLen, nil
}

// fwPutColor writes an RGB colour as three u16 BE (hue, sat, val) on the 0..4095 scale.
func fwPutColor(cfg []byte, off int, r, g, b byte) {
	h, s, v := rgbToHSV(r, g, b)
	hue := uint16(math.Round(h/360*fwScale)) % (fwScale + 1)
	sat := uint16(math.Round(s * fwScale))
	val := uint16(math.Round(v * fwScale))
	for i, x := range []uint16{hue, sat, val} {
		cfg[off+2*i] = byte(x >> 8)
		cfg[off+2*i+1] = byte(x)
	}
}

// fwSetMode sets output n's mode, keeping its fading bit.
func fwSetMode(cfg []byte, n int, mode byte) error {
	o, err := fwBlock(n)
	if err != nil {
		return err
	}
	cfg[o+fwOffMode] = cfg[o+fwOffMode]&fwFadeBit | mode&^fwFadeBit
	return nil
}

// fwApply applies a lighting selection to output n of a config buffer:
//   - "off" / "static": solid colour (black for off), as a software-sensor-4 mode with both
//     gradient colours equal
//   - "rotating": rotating colour change (colours untouched)
//   - a temperature mode (see FarbwerkLightingModes): the mode, with colours c1 (cold end)
//     and c2 (hot end); c2 defaults to c1
//
// Strip type, fading, optimised colours, refresh speed and low-pass filter are left as
// aquasuite set them, and so is the gradient range unless it is empty (fwEnsureRange).
func fwApply(cfg []byte, n int, mode string, c1, c2 *[3]byte) error {
	o, err := fwBlock(n)
	if err != nil {
		return err
	}
	var m byte
	switch mode {
	case "off":
		c1, c2 = &[3]byte{}, &[3]byte{}
		m = FwModeSoftware1 + fwSolidSensor
	case "static":
		if c1 == nil {
			return fmt.Errorf("static needs a colour")
		}
		c2 = c1
		m = FwModeSoftware1 + fwSolidSensor
	case "rotating":
		return fwSetMode(cfg, n, FwModeRotating)
	default:
		v, ok := fwTempModes[mode]
		if !ok {
			return fmt.Errorf("unknown Farbwerk mode %q", mode)
		}
		m = v
	}
	if err := fwSetMode(cfg, n, m); err != nil {
		return err
	}
	fwEnsureRange(cfg, o, mode)
	if c1 != nil {
		if c2 == nil {
			c2 = c1
		}
		fwPutColor(cfg, o+fwOffColor1, c1[0], c1[1], c1[2])
		fwPutColor(cfg, o+fwOffColor2, c2[0], c2[1], c2[2])
	}
	return nil
}

// Default gradient ranges (°C) for temperature modes whose range is empty. Rotating colour
// change leaves "gradient to" at 0 (aquasuite writes it back when leaving that mode), so
// switching straight to a temperature mode would otherwise give a 0-0 °C gradient.
var (
	fwRangeHost   = [2]float64{30, 80} // CPU / GPU
	fwRangeSensor = [2]float64{20, 50} // hardware sensors (usually water temperature)
)

// fwEnsureRange sets a default gradient range on the block at o if its range is empty or
// backwards (to <= from). A valid range set in aquasuite is kept.
func fwEnsureRange(cfg []byte, o int, mode string) {
	from := fwGet16(cfg, o+fwOffFromTemp)
	to := fwGet16(cfg, o+fwOffToTemp)
	if to > from {
		return
	}
	r := fwRangeSensor
	if mode == "cputemp" || mode == "gputemp" || mode == "static" || mode == "off" {
		r = fwRangeHost
	}
	fwPut16(cfg, o+fwOffFromTemp, uint16(r[0]*100))
	fwPut16(cfg, o+fwOffToTemp, uint16(r[1]*100))
}

// fwTempModes maps CoolerControl mode names to Farbwerk temperature modes. Software sensor 1
// carries the CPU temperature and 2 the GPU temperature (see FarbwerkSensors).
var fwTempModes = map[string]byte{
	"cputemp": FwModeSoftware1 + 0,
	"gputemp": FwModeSoftware1 + 1,
	"sensor1": FwModeSensor1 + 0,
	"sensor2": FwModeSensor1 + 1,
	"sensor3": FwModeSensor1 + 2,
	"sensor4": FwModeSensor1 + 3,
}

// FarbwerkLightingModes is the CoolerControl mode list for each Farbwerk output.
// Temperature modes require two colours (CoolerControl shows as many pickers as MinColors):
// the cold end, then the hot end of the gradient range (aquapx fw range).
func FarbwerkLightingModes() []LightingMode {
	return []LightingMode{
		{"off", "Off", 0, 0},
		{"static", "Single Colour", 1, 1},
		{"rotating", "Rotating Colour Change", 0, 0},
		{"cputemp", "CPU Temperature", 2, 2},
		{"gputemp", "GPU Temperature", 2, 2},
		{"sensor1", "Temperature Sensor 1", 2, 2},
		{"sensor2", "Temperature Sensor 2", 2, 2},
		{"sensor3", "Temperature Sensor 3", 2, 2},
		{"sensor4", "Temperature Sensor 4", 2, 2},
	}
}

// Apply applies a lighting selection to output n (1-4). See fwApply.
func (f *Farbwerk) Apply(n int, mode string, c1, c2 *[3]byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg, err := f.readConfig()
	if err != nil {
		return err
	}
	if err := fwApply(cfg, n, mode, c1, c2); err != nil {
		return err
	}
	return f.writeConfig(cfg)
}

// FarbwerkOutput summarises one output's settings.
type FarbwerkOutput struct {
	Mode           byte
	Fading         bool
	FromC, ToC     float64
	Color1, Color2 [3]uint16 // hue, sat, val on 0..4095
	StripType      byte
}

// Read returns all four outputs' settings.
func (f *Farbwerk) Read() ([fwOutputs]FarbwerkOutput, error) {
	var out [fwOutputs]FarbwerkOutput
	f.mu.Lock()
	cfg, err := f.readConfig()
	f.mu.Unlock()
	if err != nil {
		return out, err
	}
	u16 := func(i int) uint16 { return uint16(cfg[i])<<8 | uint16(cfg[i+1]) }
	for n := 1; n <= fwOutputs; n++ {
		o, _ := fwBlock(n)
		out[n-1] = FarbwerkOutput{
			Mode:      cfg[o+fwOffMode] &^ fwFadeBit,
			Fading:    cfg[o+fwOffMode]&fwFadeBit != 0,
			FromC:     float64(u16(o+fwOffFromTemp)) / 100,
			ToC:       float64(u16(o+fwOffToTemp)) / 100,
			Color1:    [3]uint16{u16(o + fwOffColor1), u16(o + fwOffColor1 + 2), u16(o + fwOffColor1 + 4)},
			Color2:    [3]uint16{u16(o + fwOffColor2), u16(o + fwOffColor2 + 2), u16(o + fwOffColor2 + 4)},
			StripType: cfg[o+fwOffStrip],
		}
	}
	return out, nil
}

// FarbwerkModeName is a readable name for a mode byte (low 7 bits).
func FarbwerkModeName(m byte) string {
	switch {
	case m <= 0x03:
		return fmt.Sprintf("temperature sensor %d", m+1)
	case m <= 0x07:
		return fmt.Sprintf("software sensor %d", m-3)
	case m == FwModeRotating:
		return "rotating colour change"
	}
	return fmt.Sprintf("unknown 0x%02x", m)
}

// fwSensorPacket builds the software sensor output report. temps holds °C per sensor; NaN
// marks a sensor as not set.
func fwSensorPacket(temps [4]float64) []byte {
	p := make([]byte, fwSensorLen)
	p[0] = fwSensorReport
	for i, t := range temps {
		v := uint16(fwSensorUnset)
		if !math.IsNaN(t) {
			p[1+i] = fwSensorSet
			c := math.Round(t * 100)
			if c < 0 {
				c = 0
			}
			if c > fwSensorUnset-1 {
				c = fwSensorUnset - 1
			}
			v = uint16(c)
		}
		p[5+2*i], p[6+2*i] = byte(v>>8), byte(v)
	}
	c := CRC16USB(p[1:13])
	p[13], p[14] = byte(c>>8), byte(c)
	return p
}

// SetSoftwareSensors sends the four software sensor temperatures (°C; NaN = not set). Like
// aquasuite, send it about once a second while the software-sensor modes are in use.
func (f *Farbwerk) SetSoftwareSensors(temps [4]float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, err := f.dev.Write(fwSensorPacket(temps))
	f.lastSensor = time.Now()
	if err != nil {
		return fmt.Errorf("software sensor report 0x04: %w", err)
	}
	return nil
}

// FarbwerkLevels holds each output's two colour brightness values (val of colour 1 and
// colour 2, 0..4095): what "All off" saves and "All on" restores.
type FarbwerkLevels [fwOutputs][2]uint16

func fwLevelOffs(n int) (int, int) {
	o, _ := fwBlock(n)
	return o + fwOffColor1 + 4, o + fwOffColor2 + 4
}

func fwGet16(b []byte, i int) uint16    { return uint16(b[i])<<8 | uint16(b[i+1]) }
func fwPut16(b []byte, i int, v uint16) { b[i], b[i+1] = byte(v>>8), byte(v) }

// fwAllOff zeroes every output's colour brightness, keeping modes, hues and saturations, and
// returns the levels it replaced. lit reports whether any output was lit; if none was (already
// off), the caller should keep its previous snapshot rather than saving zeros.
func fwAllOff(cfg []byte) (saved FarbwerkLevels, lit bool) {
	for n := 1; n <= fwOutputs; n++ {
		a, b := fwLevelOffs(n)
		saved[n-1] = [2]uint16{fwGet16(cfg, a), fwGet16(cfg, b)}
		if saved[n-1][0] != 0 || saved[n-1][1] != 0 {
			lit = true
		}
		fwPut16(cfg, a, 0)
		fwPut16(cfg, b, 0)
	}
	return saved, lit
}

// fwAllOn restores outputs that are fully off (both levels 0) to their saved levels, or to full
// brightness without a usable snapshot. Outputs set since "All off" are left alone.
func fwAllOn(cfg []byte, snap *FarbwerkLevels) {
	for n := 1; n <= fwOutputs; n++ {
		a, b := fwLevelOffs(n)
		if fwGet16(cfg, a) != 0 || fwGet16(cfg, b) != 0 {
			continue
		}
		lv := [2]uint16{fwScale, fwScale}
		if snap != nil && (snap[n-1][0] != 0 || snap[n-1][1] != 0) {
			lv = snap[n-1]
		}
		fwPut16(cfg, a, lv[0])
		fwPut16(cfg, b, lv[1])
	}
}

// AllOff turns every output off by zeroing its brightness (modes and colours are kept), saving
// the previous levels to statePath so AllOn can restore them, even after a restart.
func (f *Farbwerk) AllOff(statePath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg, err := f.readConfig()
	if err != nil {
		return err
	}
	saved, lit := fwAllOff(cfg)
	if lit && statePath != "" {
		if err := saveFarbwerkLevels(statePath, saved); err != nil {
			return fmt.Errorf("save Farbwerk levels: %w", err)
		}
	}
	return f.writeConfig(cfg)
}

// AllOn restores the outputs AllOff turned off (full brightness if nothing was saved).
func (f *Farbwerk) AllOn(statePath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg, err := f.readConfig()
	if err != nil {
		return err
	}
	var snap *FarbwerkLevels
	if statePath != "" {
		if s, err := loadFarbwerkLevels(statePath); err == nil {
			snap = &s
		}
	}
	fwAllOn(cfg, snap)
	return f.writeConfig(cfg)
}

func saveFarbwerkLevels(path string, l FarbwerkLevels) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	return saveFile(path, b)
}

func loadFarbwerkLevels(path string) (FarbwerkLevels, error) {
	var l FarbwerkLevels
	b, err := os.ReadFile(path)
	if err != nil {
		return l, err
	}
	return l, json.Unmarshal(b, &l)
}

// fwPluginDir is the CoolerControl plugin directory install.sh uses.
var fwPluginDir = "/var/lib/coolercontrol/plugins/aquapx"

// FarbwerkStatePath is where the "All off" snapshot is kept, shared by the plugin and the CLI:
// the CoolerControl plugin directory if it exists, else next to the executable.
func FarbwerkStatePath() string {
	if fi, err := os.Stat(fwPluginDir); err == nil && fi.IsDir() {
		return filepath.Join(fwPluginDir, "farbwerk-levels.json")
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "farbwerk-levels.json")
	}
	return filepath.Join(os.TempDir(), "aquapx-farbwerk-levels.json")
}

// FarbwerkAllModes is the CoolerControl mode list for the "All" channel.
func FarbwerkAllModes() []LightingMode {
	return []LightingMode{
		{"on", "On", 0, 0},
		{"off", "Off", 0, 0},
	}
}

// Strip types (block +23), from aquasuite's list.
var FarbwerkStripTypes = map[string]byte{
	"grb": 0, // 12V, Green, Red, Blue
	"brg": 1, // 12V, Blue, Red, Green
}

// FarbwerkStripName names a strip type value.
func FarbwerkStripName(v byte) string {
	switch v {
	case 0:
		return "12V, Green, Red, Blue (grb)"
	case 1:
		return "12V, Blue, Red, Green (brg)"
	}
	return fmt.Sprintf("unknown %d", v)
}

// fwModify runs fn on the config and writes it back.
func (f *Farbwerk) fwModify(fn func(cfg []byte) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg, err := f.readConfig()
	if err != nil {
		return err
	}
	if err := fn(cfg); err != nil {
		return err
	}
	return f.writeConfig(cfg)
}

// SetStripType sets output n's strip type (the colour channel order of the connected strip).
func (f *Farbwerk) SetStripType(n int, t byte) error {
	return f.fwModify(func(cfg []byte) error {
		o, err := fwBlock(n)
		if err != nil {
			return err
		}
		cfg[o+fwOffStrip] = t
		return nil
	})
}

// SetRange sets output n's gradient range in °C: colour 1 at from, colour 2 at to.
func (f *Farbwerk) SetRange(n int, from, to float64) error {
	if from < 0 || to > 300 || to <= from {
		return fmt.Errorf("bad range %.1f-%.1f °C (need 0 <= from < to <= 300)", from, to)
	}
	return f.fwModify(func(cfg []byte) error {
		o, err := fwBlock(n)
		if err != nil {
			return err
		}
		fwPut16(cfg, o+fwOffFromTemp, uint16(math.Round(from*100)))
		fwPut16(cfg, o+fwOffToTemp, uint16(math.Round(to*100)))
		return nil
	})
}
