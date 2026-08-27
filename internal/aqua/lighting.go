package aqua

// LightingMode describes a lighting mode for CoolerControl's UI: how many base colours it
// takes (0 for self-coloured effects like rainbow, 1 for a fixed/base colour).
type LightingMode struct {
	Name      string
	Label     string
	MinColors uint32
	MaxColors uint32
}

// LightingModes is the catalogue coolerlink-style plugins advertise to CoolerControl. It maps
// onto the D5 Next's onboard effects (a single base colour is supported; multi-colour effect
// sequences would need more of the config decoded).
func LightingModes() []LightingMode {
	return []LightingMode{
		{"status", "System Status (match fans)", 0, 0}, // GPU-load driven: white idle / theme-orange busy
		{"off", "Off", 0, 0},
		{"static", "Static Colour", 1, 1},
		{"breathing", "Breathing", 1, 1},
		{"rainbow", "Rainbow", 0, 0},
		{"colorchange", "Colour Change", 0, 0},
		{"colorshift", "Colour Shift", 1, 1},
		{"colorsequence", "Colour Sequence", 1, 1},
		{"blinking", "Blinking", 1, 1},
		{"scanner", "Scanner", 1, 1},
		{"laser", "Laser", 1, 1},
		{"wave", "Wave", 1, 1},
		{"flame", "Flame", 0, 0},
		{"rain", "Rain", 1, 1},
		{"swipingrainbow", "Swiping Rainbow", 0, 0},
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
