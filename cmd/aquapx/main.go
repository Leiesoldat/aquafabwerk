// Command aquapx controls the RGB lighting of Aquacomputer devices on Linux.
// Currently: the D5 Next pump (fixed colour + onboard effects), the High Flow Next and the
// LEAKSHIELD (colour), via read-modify-write of their HID config reports. See ~/src/aquapx-re/PROTOCOL.md for the reverse-engineered protocol.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"aquapx/internal/aqua"
)

var names = map[string][3]byte{
	"red": {255, 0, 0}, "green": {0, 255, 0}, "blue": {0, 0, 255},
	"white": {255, 255, 255}, "cyan": {0, 255, 255}, "magenta": {255, 0, 255},
	"yellow": {255, 255, 0}, "orange": {255, 90, 0}, "purple": {160, 0, 255},
	"pink": {255, 40, 120}, "off": {0, 0, 0},
}

func parseColor(s string) ([3]byte, error) {
	if c, ok := names[strings.ToLower(s)]; ok {
		return c, nil
	}
	s = strings.TrimPrefix(strings.ToLower(s), "#")
	if len(s) == 6 {
		if n, err := strconv.ParseUint(s, 16, 32); err == nil {
			return [3]byte{byte(n >> 16), byte(n >> 8), byte(n)}, nil
		}
	}
	return [3]byte{}, fmt.Errorf("bad colour %q (use RRGGBB hex or a name)", s)
}

func main() {
	args := os.Args[1:]
	if len(args) < 2 || (args[0] != "d5" && args[0] != "hfn" && args[0] != "ls" && args[0] != "fw") {
		usage()
	}
	if args[0] == "fw" {
		runFarbwerk(args[1:])
		return
	}
	if args[0] == "hfn" {
		runHFN(args[1:])
		return
	}
	if args[0] == "ls" {
		runLeakshield(args[1:])
		return
	}
	cmd := args[1]

	d, err := aqua.OpenD5Next()
	if err != nil {
		fatal(err)
	}
	defer d.Close()

	switch cmd {
	case "color":
		if len(args) < 3 {
			fatal(fmt.Errorf("usage: aquapx d5 color <RRGGBB|name>"))
		}
		c, err := parseColor(args[2])
		if err != nil {
			fatal(err)
		}
		if err := d.SetColor(c[0], c[1], c[2]); err != nil {
			fatal(err)
		}
		fmt.Printf("D5 Next -> solid #%02x%02x%02x\n", c[0], c[1], c[2])

	case "effect":
		if len(args) < 3 {
			fatal(fmt.Errorf("usage: aquapx d5 effect <name> [RRGGBB|name]"))
		}
		col := [3]byte{255, 255, 255}
		if len(args) >= 4 {
			if c, err := parseColor(args[3]); err == nil {
				col = c
			}
		}
		if err := d.SetEffect(args[2], col[0], col[1], col[2]); err != nil {
			fatal(err)
		}
		fmt.Printf("D5 Next -> effect %s\n", args[2])

	case "off":
		if err := d.Off(); err != nil {
			fatal(err)
		}
		fmt.Println("D5 Next -> off")

	case "dump":
		s, _, err := d.Read()
		if err != nil {
			fatal(err)
		}
		fmt.Printf("D5 Next: on=%v effect=0x%02x (%s) hue=0x%04x sat=%d brightness=%d\n",
			s.On, s.Effect, s.EffectName, s.Hue, s.Saturation, s.Brightness)

	case "rgbpx", "strip":
		runStrip(d, args[2:])

	case "brightness":
		if len(args) < 3 {
			pct, err := d.Brightness()
			if err != nil {
				fatal(err)
			}
			fmt.Printf("D5 Next brightness: %d%%\n", pct)
			return
		}
		pct, err := strconv.Atoi(strings.TrimSuffix(args[2], "%"))
		if err != nil || pct < 0 || pct > 100 {
			fatal(fmt.Errorf("usage: aquapx d5 brightness [0-100]"))
		}
		if err := d.SetBrightness(pct); err != nil {
			fatal(err)
		}
		fmt.Printf("D5 Next -> brightness %d%%\n", pct)

	case "effects":
		var es []string
		for n := range aqua.Effects {
			es = append(es, n)
		}
		sort.Strings(es)
		fmt.Println("effects:", strings.Join(es, ", "))

	default:
		usage()
	}
}

// runHFN handles the High Flow Next subcommands. Only hue is decoded for this device, so it
// recolours the running flow-indicator effect rather than switching effects.
func runHFN(args []string) {
	f, err := aqua.OpenHighFlowNext()
	if err != nil {
		fatal(err)
	}
	defer f.Close()
	switch args[0] {
	case "color":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: aquapx hfn color <RRGGBB|name>"))
		}
		c, err := parseColor(args[1])
		if err != nil {
			fatal(err)
		}
		if err := f.SetColor(c[0], c[1], c[2]); err != nil {
			fatal(err)
		}
		fmt.Printf("High Flow Next -> #%02x%02x%02x\n", c[0], c[1], c[2])
	case "dump":
		hs, err := f.Records()
		if err != nil {
			fatal(err)
		}
		fmt.Printf("High Flow Next: %d colour records (hue,sat,val)=%v\n", len(hs), hs)
	default:
		usage()
	}
}

// runLeakshield handles the LEAKSHIELD subcommands. Colour (hue/sat/brightness) is decoded from
// aquasuite captures; "probe" is a read-only GET_FEATURE that saves the raw report so the live
// layout can be confirmed before the first write.
func runLeakshield(args []string) {
	l, err := aqua.OpenLeakshield()
	if err != nil {
		fatal(err)
	}
	defer l.Close()
	fmt.Fprintf(os.Stderr, "opened LEAKSHIELD %s (HID interface %d)\n", l.Path, l.Interface)
	switch args[0] {
	case "color":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: aquapx ls color <RRGGBB|name>"))
		}
		c, err := parseColor(args[1])
		if err != nil {
			fatal(err)
		}
		if err := l.SetColor(c[0], c[1], c[2]); err != nil {
			fatal(err)
		}
		fmt.Printf("LEAKSHIELD -> #%02x%02x%02x\n", c[0], c[1], c[2])

	case "off":
		if err := l.Off(); err != nil {
			fatal(err)
		}
		fmt.Println("LEAKSHIELD -> off (brightness 0)")

	case "effect":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: aquapx ls effect <name> [RRGGBB|name]  (see 'aquapx ls effects')"))
		}
		c := [3]byte{0xff, 0x00, 0x00}
		if len(args) >= 3 {
			if c, err = parseColor(args[2]); err != nil {
				fatal(err)
			}
		}
		if err := l.SetEffect(args[1], c[0], c[1], c[2]); err != nil {
			fatal(err)
		}
		fmt.Printf("LEAKSHIELD -> effect %s (base #%02x%02x%02x)\n", args[1], c[0], c[1], c[2])

	case "effects":
		for _, m := range aqua.LeakshieldLightingModes() {
			if m.Name == "off" || m.Name == "status" {
				continue
			}
			note := "self-coloured"
			if m.MinColors > 0 {
				note = "takes a base colour"
			}
			fmt.Printf("  %-15s %-18s %s\n", m.Name, m.Label, note)
		}

	case "dump":
		s, _, err := l.Read()
		if err != nil {
			fatal(err)
		}
		eff := s.Effect
		if eff == "" {
			eff = "unknown"
		}
		fmt.Printf("LEAKSHIELD: effect=%s (0x%02x) hue=0x%04x (%.1f deg) sat=%d brightness=%d\n",
			eff, s.EffectByte, s.Hue, s.HueDeg, s.Saturation, s.Brightness)

	case "probe":
		// aquapx ls probe [report-id-hex] [out-file]   (read-only)
		rid := byte(0x03)
		if len(args) >= 2 {
			n, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(args[1]), "0x"), 16, 8)
			if err != nil {
				fatal(fmt.Errorf("bad report id %q", args[1]))
			}
			rid = byte(n)
		}
		out := fmt.Sprintf("leakshield_report%02x.bin", rid)
		if len(args) >= 3 {
			out = args[2]
		}
		fmt.Fprintf(os.Stderr, "reading feature report 0x%02x (read-only)...\n", rid)
		p, err := l.Probe(rid)
		if err != nil {
			fatal(err)
		}
		if err := os.WriteFile(out, p.Data, 0o644); err != nil {
			fatal(fmt.Errorf("got %d bytes but could not save %s: %w", p.N, out, err))
		}
		if abs, err := filepath.Abs(out); err == nil {
			out = abs
		}
		fmt.Printf("report 0x%02x: %d bytes (expected 750 for 0x03: %v)\n", rid, p.N, p.LenOK)
		fmt.Printf("CRC-16/USB over [1:%d]: calc=%04x stored=%04x -> %s\n",
			max(p.N-2, 0), p.CRCCalc, p.CRCStore, map[bool]string{true: "OK", false: "MISMATCH"}[p.CRCOK])
		if rid == 0x03 && p.LenOK && p.CRCOK {
			d := p.Data
			hue := uint16(d[620])<<8 | uint16(d[621])
			fmt.Printf("colour @620: hue=0x%04x (%.1f deg) sat=%d brightness=%d\n",
				hue, float64(hue)/1536*360, d[622], d[623])
			fmt.Println("layout CONFIRMED: matches the captured writes; 'ls color' is safe to use")
		}
		fmt.Printf("raw report saved -> %s\n\n", out)
		hexdump(p.Data)

	default:
		usage()
	}
}

// hexdump prints b like Wireshark's hex pane (16 bytes per row, offset prefix).
func hexdump(b []byte) {
	for i := 0; i < len(b); i += 16 {
		end := min(i+16, len(b))
		fmt.Printf("%04x  % x\n", i, b[i:end])
	}
}

// runStrip handles "aquapx d5 rgbpx ..." (alias "strip"): the D5 Next's RGBpx LED output,
// independent of the pump ring.
func runStrip(d *aqua.D5Next, args []string) {
	if len(args) < 1 {
		usage()
	}
	switch args[0] {
	case "color":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: aquapx d5 rgbpx color <RRGGBB|name>"))
		}
		c, err := parseColor(args[1])
		if err != nil {
			fatal(err)
		}
		if err := d.StripSetColor(c[0], c[1], c[2]); err != nil {
			fatal(err)
		}
		fmt.Printf("D5 Next RGBpx LED -> solid #%02x%02x%02x\n", c[0], c[1], c[2])
	case "effect":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: aquapx d5 rgbpx effect <name> [RRGGBB|name]"))
		}
		col := [3]byte{255, 255, 255}
		if len(args) >= 3 {
			c, err := parseColor(args[2])
			if err != nil {
				fatal(err)
			}
			col = c
		}
		if err := d.StripSetEffect(args[1], col[0], col[1], col[2]); err != nil {
			fatal(err)
		}
		fmt.Printf("D5 Next RGBpx LED -> effect %s\n", args[1])
	case "off":
		if err := d.StripOff(); err != nil {
			fatal(err)
		}
		fmt.Println("D5 Next RGBpx LED -> off")
	case "dump":
		s, err := d.StripRead()
		if err != nil {
			fatal(err)
		}
		fmt.Printf("D5 Next RGBpx LED: effect=0x%02x (%s) hue=0x%04x sat=%d value=%d\n",
			s.Effect, s.EffectName, s.Hue, s.Saturation, s.Value)
	default:
		usage()
	}
}

// runFarbwerk handles "aquapx fw ...": the original Farbwerk's four RGB outputs.
func runFarbwerk(args []string) {
	if args[0] == "sensors" { // read-only, no device needed
		t := aqua.FarbwerkSensors()
		fmt.Printf("software sensors aquapx feeds the Farbwerk: 1 CPU=%.1f°C  2 GPU=%.1f°C  3 unset  4 solid-colour constant=%.1f°C\n",
			t[0], t[1], t[3])
		return
	}
	f, err := aqua.OpenFarbwerk()
	if err != nil {
		fatal(err)
	}
	defer f.Close()
	switch args[0] {
	case "dump":
		outs, err := f.Read()
		if err != nil {
			fatal(err)
		}
		fmt.Printf("config source: %s\n", map[string]string{
			"device":   "read from the Farbwerk",
			"saved":    "aquapx's saved copy of the last write (the Farbwerk can't be read)",
			"template": "built-in template (the Farbwerk can't be read and nothing was written yet)",
		}[f.Source])
		for i, o := range outs {
			fmt.Printf("output %d: %s fading=%v gradient %.1f-%.1f°C colour1=%04x/%04x/%04x colour2=%04x/%04x/%04x strip=%d\n",
				i+1, aqua.FarbwerkModeName(o.Mode), o.Fading, o.FromC, o.ToC,
				o.Color1[0], o.Color1[1], o.Color1[2], o.Color2[0], o.Color2[1], o.Color2[2], o.StripType)
		}
	case "set":
		if len(args) < 3 {
			fatal(fmt.Errorf("usage: aquapx fw set <1-4> <mode> [colour1] [colour2]  (modes: %s)", fwModeNames()))
		}
		n, err := strconv.Atoi(args[1])
		if err != nil {
			fatal(fmt.Errorf("bad output %q", args[1]))
		}
		var c1, c2 *[3]byte
		if len(args) >= 4 {
			c, err := parseColor(args[3])
			if err != nil {
				fatal(err)
			}
			c1 = &c
		}
		if len(args) >= 5 {
			c, err := parseColor(args[4])
			if err != nil {
				fatal(err)
			}
			c2 = &c
		}
		if err := f.Apply(n, args[2], c1, c2); err != nil {
			fatal(err)
		}
		fmt.Printf("Farbwerk output %d -> %s\n", n, args[2])
		if strings.HasSuffix(args[2], "temp") || args[2] == "static" || args[2] == "off" {
			fmt.Println("note: this mode follows a software sensor; run the plugin (or 'aquapx fw feed') to keep it fed")
		}
	case "all":
		if len(args) < 2 || (args[1] != "on" && args[1] != "off") {
			fatal(fmt.Errorf("usage: aquapx fw all on|off"))
		}
		path := aqua.FarbwerkStatePath()
		if args[1] == "off" {
			err = f.AllOff(path)
		} else {
			err = f.AllOn(path)
		}
		if err != nil {
			fatal(err)
		}
		fmt.Printf("Farbwerk: all outputs %s\n", args[1])
	case "feed":
		fmt.Println("feeding Farbwerk software sensors once a second (Ctrl-C to stop)")
		aqua.RunFarbwerkSensors(f, make(chan struct{}))
	default:
		usage()
	}
}

func fwModeNames() string {
	var ns []string
	for _, m := range aqua.FarbwerkLightingModes() {
		ns = append(ns, m.Name)
	}
	return strings.Join(ns, ", ")
}

func usage() {
	fmt.Fprintln(os.Stderr, `aquapx — Aquacomputer RGB control

usage:
  aquapx d5  color <RRGGBB|name>       solid colour (static effect)
  aquapx d5  effect <name> [colour]    onboard effect (see 'effects')
  aquapx d5  off                       LEDs off
  aquapx d5  brightness [0-100]        show / set brightness (pump ring + RGBpx LEDs)
  aquapx d5  dump                      show current LED state
  aquapx d5  effects                   list effect names (shared by pump and RGBpx LED)
  aquapx d5  rgbpx color <RRGGBB|name> RGBpx LED output: solid colour
  aquapx d5  rgbpx effect <name> [col] RGBpx LED output: onboard effect
  aquapx d5  rgbpx off                 RGBpx LED output: colour at brightness 0
  aquapx d5  rgbpx dump                RGBpx LED output: show effect/colour
  aquapx hfn color <RRGGBB|name>       recolour the flow indicator
  aquapx hfn dump                      show palette hues
  aquapx ls  color <RRGGBB|name>       LEAKSHIELD colour (hue/sat/brightness)
  aquapx ls  effect <name> [color]     LEAKSHIELD onboard effect (base colour default red)
  aquapx ls  effects                   list LEAKSHIELD effects
  aquapx ls  off                       LEAKSHIELD brightness 0
  aquapx ls  dump                      show LEAKSHIELD colour state
  aquapx fw  dump                      Farbwerk: show all four outputs
  aquapx fw  set <1-4> <mode> [c1] [c2] Farbwerk output mode (see 'set' usage for modes)
  aquapx fw  all on|off                all outputs off (brightness 0) / back on
  aquapx fw  sensors                   show the CPU/GPU temps aquapx feeds the Farbwerk
  aquapx fw  feed                      feed the Farbwerk's software sensors (foreground)
  aquapx ls  probe [rid] [file]        READ-ONLY: GET_FEATURE (default 0x03), save + hexdump`)
	os.Exit(2)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "aquapx:", err)
	os.Exit(1)
}
