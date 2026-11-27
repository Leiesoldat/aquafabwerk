package aqua

// LightingMode describes a lighting mode for CoolerControl's UI: how many base colours it
// takes (0 for self-coloured effects like rainbow, 1 for a fixed/base colour).
type LightingMode struct {
	Name      string
	Label     string
	MinColors uint32
	MaxColors uint32
}

// LightingModes is the D5 Next's CoolerControl mode catalogue, in aquasuite's order. Effects
// that take a base colour show a colour picker; self-coloured ones (rainbows, flame, the
// sensor-driven Colour Switch / Bar Graph) use aquasuite's captured palette. The sound effects
// and AMBIENTpx need audio/screen data streamed from the PC, so they're CLI-only (see
// d5HostDriven).
func LightingModes() []LightingMode {
	return []LightingMode{
		{"status", "System Status (match fans)", 0, 0}, // GPU-load driven: white idle / theme-orange busy
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
		{"rain", "Rain", 1, 1},
		{"snowfall", "Snowfall", 1, 1},
		{"stardust", "Stardust", 1, 1},
		{"colorswitch", "Colour Switch", 0, 0},
		{"bargraph", "Bar Graph", 0, 0},
	}
}

// ApplyLighting applies a CoolerControl lighting selection (mode + base colour) to the pump.
func (d *D5Next) ApplyLighting(mode string, r, g, b byte) error {
	switch mode {
	case "off":
		return d.Off()
	case "static":
		return d.SetColor(r, g, b)
	default:
		return d.SetEffect(mode, r, g, b)
	}
}
