package aqua

import (
	"bufio"
	"encoding/hex"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testdata/farbwerk_writes.txt: "<capture>-<n> <hex>" for every aquasuite config write in the
// Farbwerk captures, in order. All changes were made on RGB output 4.
func loadFW(t *testing.T) (map[string][]byte, []string) {
	t.Helper()
	f, err := os.Open("testdata/farbwerk_writes.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, order := map[string][]byte{}, []string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, h, _ := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		b, err := hex.DecodeString(h)
		if err != nil || len(b) != fwLen {
			t.Fatalf("%s: bad fixture", k)
		}
		m[k] = b
		order = append(order, k)
	}
	return m, order
}

func fwCopy(b []byte) []byte { return append([]byte(nil), b...) }

func TestFarbwerkCapturedCRC(t *testing.T) {
	fx, order := loadFW(t)
	for _, k := range order {
		if !fwCRCOK(fx[k]) {
			t.Errorf("%s: CRC invalid", k)
		}
	}
	if CRC16USB(fwSaveReport[1:9]) != uint16(fwSaveReport[9])<<8|uint16(fwSaveReport[10]) {
		t.Error("save report CRC invalid")
	}
}

// Stepping output 4's mode reproduces aquasuite's writes: sensors 2-4 + software sensors
// 1-3 (sensorlist), software sensors 1-4 (software).
func TestFarbwerkModeSteps(t *testing.T) {
	fx, _ := loadFW(t)
	o, _ := fwBlock(4)
	for _, run := range []struct {
		tag string
		n   int
	}{{"sensorlist", 7}, {"software", 4}} {
		for i := 2; i <= run.n; i++ {
			prev, want := fx[run.tag+"-"+itoa(i-1)], fx[run.tag+"-"+itoa(i)]
			got := fwCopy(prev)
			if err := fwSetMode(got, 4, want[o]); err != nil {
				t.Fatal(err)
			}
			fwFixCRC(got)
			if string(got) != string(want) {
				t.Errorf("%s-%d: mode step doesn't reproduce the capture", run.tag, i)
			}
		}
	}
	if fx["sensorlist-1"][o] != 0x01 || fx["software-4"][o] != 0x07 || fx["rotating-1"][o] != FwModeRotating {
		t.Error("mode values don't match the decoded table")
	}
}

// Mode changes keep the fading bit (bit 7), which aquasuite toggles on its own.
func TestFarbwerkFadingBit(t *testing.T) {
	fx, _ := loadFW(t)
	o, _ := fwBlock(4)
	on := fx["options-7"]
	if on[o] != 0x84 || fx["options-8"][o] != 0x04 {
		t.Fatalf("fixture fading bytes % x / % x", on[o], fx["options-8"][o])
	}
	got := fwCopy(on)
	_ = fwSetMode(got, 4, FwModeSoftware1+1)
	if got[o] != 0x85 {
		t.Errorf("fading bit lost: 0x%02x", got[o])
	}
}

// Colour encoding matches aquasuite: red/green/blue hue writes, 80FF00 and full sat/val.
func TestFarbwerkColour(t *testing.T) {
	fx, _ := loadFW(t)
	o, _ := fwBlock(4)
	for _, c := range []struct {
		prev, want string
		rgb        [3]byte
	}{{"options-1", "options-2", [3]byte{0, 255, 0}}, {"options-2", "options-3", [3]byte{0, 0, 255}}} {
		got := fwCopy(fx[c.prev])
		fwPutColor(got, o+fwOffColor1, c.rgb[0], c.rgb[1], c.rgb[2])
		fwFixCRC(got)
		if string(got) != string(fx[c.want]) {
			t.Errorf("%s: colour %v doesn't reproduce the capture", c.want, c.rgb)
		}
	}
	buf := make([]byte, 6)
	fwPutColor(buf, 0, 255, 0, 0)
	if hex.EncodeToString(buf) != hex.EncodeToString(fx["options-1"][o+fwOffColor1:o+fwOffColor1+6]) {
		t.Errorf("red = % x", buf)
	}
	fwPutColor(buf, 0, 0x80, 0xff, 0x00)
	if hex.EncodeToString(buf) != "03fe0fff0fff" {
		t.Errorf("80FF00 = % x, want 03fe 0fff 0fff", buf)
	}
}

// fwApply touches only the chosen output's mode and colours.
func TestFarbwerkApply(t *testing.T) {
	fx, _ := loadFW(t)
	base := fx["options2-7"]
	for n := 1; n <= fwOutputs; n++ {
		o, _ := fwBlock(n)
		for _, m := range FarbwerkLightingModes() {
			got := fwCopy(base)
			c1, c2 := &[3]byte{255, 0, 0}, &[3]byte{0, 0, 255}
			if err := fwApply(got, n, m.Name, c1, c2); err != nil {
				t.Fatalf("%s: %v", m.Name, err)
			}
			for i := range got {
				inMode := i == o+fwOffMode
				inColours := i >= o+fwOffColor1 && i < o+fwOffColor2+6
				inRange := i >= o+fwOffFromTemp && i < o+fwOffToTemp+2
				if !inMode && !inColours && !inRange && got[i] != base[i] {
					t.Errorf("output %d %s: byte %d changed", n, m.Name, i)
				}
			}
			if got[o]&fwFadeBit != base[o]&fwFadeBit {
				t.Errorf("output %d %s: fading bit changed", n, m.Name)
			}
			switch m.Name {
			case "static":
				if got[o]&^fwFadeBit != FwModeSoftware1+fwSolidSensor ||
					string(got[o+fwOffColor1:o+fwOffColor1+6]) != string(got[o+fwOffColor2:o+fwOffColor2+6]) {
					t.Errorf("output %d static: not a solid colour", n)
				}
			case "off":
				if got[o+fwOffColor1+4] != 0 || got[o+fwOffColor1+5] != 0 || got[o+fwOffColor2+4] != 0 || got[o+fwOffColor2+5] != 0 {
					t.Errorf("output %d off: colours not black", n)
				}
			case "rotating":
				if got[o]&^fwFadeBit != FwModeRotating {
					t.Errorf("output %d rotating: mode 0x%02x", n, got[o])
				}
				if string(got[o+fwOffColor1:o+fwOffColor2+6]) != string(base[o+fwOffColor1:o+fwOffColor2+6]) {
					t.Errorf("output %d rotating: colours changed", n)
				}
			}
		}
	}
	if err := fwApply(fwCopy(base), 5, "static", &[3]byte{1, 2, 3}, nil); err == nil {
		t.Error("output 5 accepted")
	}
}

// The software sensor report matches aquasuite's packets (values in °C x 100, 7fff unset).
func TestFarbwerkSensorPacket(t *testing.T) {
	nan := math.NaN()
	for _, c := range []struct {
		temps [4]float64
		want  string
	}{
		{[4]float64{0.53, nan, nan, nan}, "040600000000357fff7fff7fffb9a0"},
		{[4]float64{1.69, 24, 26, 43}, "040606060600a909600a2810cc1c71"},
		{[4]float64{1.19, 23, 26, 44}, "0406060606007708fc0a2811300d9f"},
	} {
		if got := hex.EncodeToString(fwSensorPacket(c.temps)); got != c.want {
			t.Errorf("%v: got %s want %s", c.temps, got, c.want)
		}
	}
}

func TestHwmonTemp(t *testing.T) {
	root := t.TempDir()
	old := hwmonRoot
	hwmonRoot = root
	defer func() { hwmonRoot = old }()
	write := func(p, s string) {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "hwmon0", "name"), "nvme\n")
	write(filepath.Join(root, "hwmon0", "temp1_input"), "40000\n")
	write(filepath.Join(root, "hwmon1", "name"), "k10temp\n")
	write(filepath.Join(root, "hwmon1", "temp1_input"), "55125\n")
	write(filepath.Join(root, "hwmon1", "temp1_label"), "Tctl\n")
	write(filepath.Join(root, "hwmon1", "temp3_input"), "50000\n")
	write(filepath.Join(root, "hwmon1", "temp3_label"), "Tccd1\n")
	if got := CPUTemp(); got != 55.125 {
		t.Errorf("CPUTemp = %v", got)
	}
	if !math.IsNaN(hwmonTemp("amdgpu", "edge")) {
		t.Error("missing driver should be NaN")
	}
}

func itoa(i int) string { return string(rune('0' + i)) }

// "All off" zeroes only the colour brightness of every output and "All on" restores it,
// through the saved snapshot file.
func TestFarbwerkAllOffOn(t *testing.T) {
	fx, _ := loadFW(t)
	base := fx["options2-7"]
	cfg := fwCopy(base)
	saved, lit := fwAllOff(cfg)
	if !lit {
		t.Fatal("capture has lit outputs")
	}
	for n := 1; n <= fwOutputs; n++ {
		a, b := fwLevelOffs(n)
		if fwGet16(cfg, a) != 0 || fwGet16(cfg, b) != 0 {
			t.Errorf("output %d still lit", n)
		}
	}
	for i := range cfg {
		isLevel := false
		for n := 1; n <= fwOutputs; n++ {
			a, b := fwLevelOffs(n)
			if i == a || i == a+1 || i == b || i == b+1 {
				isLevel = true
			}
		}
		if !isLevel && cfg[i] != base[i] {
			t.Errorf("All off changed byte %d", i)
		}
	}
	path := filepath.Join(t.TempDir(), "levels.json")
	if err := saveFarbwerkLevels(path, saved); err != nil {
		t.Fatal(err)
	}
	// A second "All off" finds nothing lit and must not replace the snapshot with zeros.
	if _, lit := fwAllOff(fwCopy(cfg)); lit {
		t.Error("already-off config reported lit")
	}
	snap, err := loadFarbwerkLevels(path)
	if err != nil {
		t.Fatal(err)
	}
	fwAllOn(cfg, &snap)
	if string(cfg) != string(base) {
		t.Error("All on didn't restore the original config")
	}
	// Without a snapshot, off outputs come back at full brightness; lit ones are left alone.
	cfg2 := fwCopy(base)
	fwAllOff(cfg2)
	a4, _ := fwLevelOffs(4)
	fwPut16(cfg2, a4, 0x0123)
	fwAllOn(cfg2, nil)
	a1, b1 := fwLevelOffs(1)
	if fwGet16(cfg2, a1) != fwScale || fwGet16(cfg2, b1) != fwScale {
		t.Error("no snapshot: output 1 not at full brightness")
	}
	if fwGet16(cfg2, a4) != 0x0123 {
		t.Error("output 4, set after All off, was overwritten")
	}
}

// Without a device read, the config comes from the saved copy, else the template; a damaged
// copy falls back to the template.
func TestFarbwerkStoredConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "farbwerk-config.hex")
	var src string
	tpl, err := fwStoredConfig(path, &src)
	if err != nil || src != "template" {
		t.Fatalf("no saved copy: src=%q err=%v", src, err)
	}
	o4, _ := fwBlock(4)
	if tpl[o4+fwOffStrip] != 0 {
		t.Errorf("template output 4 strip type = %d, want 0 (Green/Red/Blue)", tpl[o4+fwOffStrip])
	}
	_ = fwApply(tpl, 4, "static", &[3]byte{0, 0, 255}, nil)
	fwFixCRC(tpl)
	if err := saveFile(path, []byte(hex.EncodeToString(tpl)+"\n")); err != nil {
		t.Fatal(err)
	}
	got, err := fwStoredConfig(path, &src)
	if err != nil || src != "saved" || string(got) != string(tpl) {
		t.Errorf("saved copy not returned: src=%q err=%v", src, err)
	}
	_ = os.WriteFile(path, []byte("00ff"), 0o644)
	if _, err := fwStoredConfig(path, &src); err != nil || src != "template" {
		t.Errorf("damaged copy: src=%q err=%v", src, err)
	}
}

// Leaving Rotating Colour Change (gradient "to" = 0) for a temperature mode gets a usable
// range, as aquasuite writes one; a valid range is kept.
func TestFarbwerkRangeAfterRotating(t *testing.T) {
	fx, _ := loadFW(t)
	o, _ := fwBlock(4)
	rot := fx["rotating-1"]
	if fwGet16(rot, o+fwOffToTemp) != 0 {
		t.Fatalf("fixture: rotating gradient-to = %#x, want 0", fwGet16(rot, o+fwOffToTemp))
	}
	got := fwCopy(rot)
	if err := fwApply(got, 4, "cputemp", &[3]byte{0, 0, 255}, &[3]byte{255, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if f, to := fwGet16(got, o+fwOffFromTemp), fwGet16(got, o+fwOffToTemp); f != 3000 || to != 8000 {
		t.Errorf("cputemp after rotating: range %d-%d, want 3000-8000", f, to)
	}
	got = fwCopy(rot)
	_ = fwApply(got, 4, "sensor1", &[3]byte{0, 0, 255}, nil)
	if f, to := fwGet16(got, o+fwOffFromTemp), fwGet16(got, o+fwOffToTemp); f != 2000 || to != 5000 {
		t.Errorf("sensor1 after rotating: range %d-%d, want 2000-5000", f, to)
	}
	valid := fx["software-1"] // aquasuite's own 0-50 °C range
	got = fwCopy(valid)
	_ = fwApply(got, 4, "gputemp", &[3]byte{0, 255, 0}, nil)
	if string(got[o+fwOffFromTemp:o+fwOffToTemp+2]) != string(valid[o+fwOffFromTemp:o+fwOffToTemp+2]) {
		t.Error("a valid range was overwritten")
	}
}

// Temperature modes require two colours, so CoolerControl shows both pickers.
func TestFarbwerkTempModesTwoColours(t *testing.T) {
	for _, m := range FarbwerkLightingModes() {
		if _, temp := fwTempModes[m.Name]; temp && (m.MinColors != 2 || m.MaxColors != 2) {
			t.Errorf("%s: colours %d-%d, want 2-2", m.Name, m.MinColors, m.MaxColors)
		}
	}
}
