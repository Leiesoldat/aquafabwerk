// Command aquapx controls the RGB lighting of Aquacomputer devices on Linux.
// Currently: the D5 Next pump (fixed colour + onboard effects), via read-modify-write of
// its HID config report. See ~/src/aquapx-re/PROTOCOL.md for the reverse-engineered protocol.
package main

import (
	"fmt"
	"os"
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
	if len(args) < 2 || (args[0] != "d5" && args[0] != "hfn") {
		usage()
	}
	if args[0] == "hfn" {
		runHFN(args[1:])
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

func usage() {
	fmt.Fprintln(os.Stderr, `aquapx — Aquacomputer RGB control

usage:
  aquapx d5  color <RRGGBB|name>       solid colour (static effect)
  aquapx d5  effect <name> [colour]    onboard effect (see 'effects')
  aquapx d5  off                       LEDs off
  aquapx d5  dump                      show current LED state
  aquapx d5  effects                   list effect names
  aquapx hfn color <RRGGBB|name>       recolour the flow indicator
  aquapx hfn dump                      show palette hues`)
	os.Exit(2)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "aquapx:", err)
	os.Exit(1)
}
