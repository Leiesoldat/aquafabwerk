## Development Disclaimer

Code development utilizes Anthropic's Claude LLM. I'm not a developer so I can only verify
code generation through actual personal system usage. Please review the code commits before
utilizing this plugin on your system. I'm not responsible if something breaks in your
environment.

# aquapx

Linux RGB control for **Aquacomputer** devices — currently the **D5 Next** pump — via their
USB-HID config protocol. A sibling to [coolerlink](https://github.com/hirparak/coolerlink):
where coolerlink drives the Corsair iCUE Link hub, aquapx drives Aquacomputer RGB that
`liquidctl` and OpenRGB don't (their D5 Next / High Flow Next RGB tickets are still open).

Ships as both a **CLI** and a **CoolerControl device-service plugin** (RGB from the CC UI).

## How it works

Aquacomputer devices have an **onboard effect engine**: RGB settings live inside the device's
big config feature report (D5 Next: report `0x03`, 809 bytes), which also holds pump/fan
config. aquapx does a **read-modify-write** — read the live config, change only the LED fields,
recompute the **CRC-16/USB**, write it back, then send the constant save report. A read's CRC
is self-checked first, which also confirms the field offsets match the unit before any write.

Colour is stored as **HSV**: hue (uint16 BE, 1536/360°), saturation, and master brightness,
plus an effect selector (static / breathing / rainbow / …). Full protocol: [`re/PROTOCOL.md`](re/PROTOCOL.md).

Reverse-engineered from the 21 USB captures in [OpenRGB issue #2111](https://gitlab.com/CalcProgrammer1/OpenRGB/-/issues/2111)
(decoder in [`re/decode.py`](re/decode.py)); no Windows/aquasuite needed.

## CLI

```
aquapx d5 color <RRGGBB|name>     # solid colour (static effect)
aquapx d5 effect <name> [colour]  # onboard effect (breathing, rainbow, scanner, flame, …)
aquapx d5 off
aquapx d5 dump                    # current LED state
aquapx d5 effects                 # list effect names
aquapx ls probe [rid] [file]      # LEAKSHIELD: read-only GET_FEATURE (default 0x03), save + hexdump
aquapx ls color <RRGGBB|name>     # LEAKSHIELD colour (refuses unless the live read validates)
aquapx ls effect <name> [colour]  # LEAKSHIELD onboard effect (see: aquapx ls effects)
aquapx ls effects                 # list LEAKSHIELD effects
aquapx ls off                     # LEAKSHIELD brightness 0
aquapx ls dump                    # LEAKSHIELD colour state
```

## CoolerControl plugin

Installs to `/var/lib/coolercontrol/plugins/aquapx/`; coolercontrold launches it and exposes
the pump as a **lighting device**. The pump's sensors continue to come from the kernel
`aquacomputer_d5next` driver separately — this plugin only adds the lighting.

```
go build -o bin/aquapx-plugin ./cmd/aquapx-plugin
sudo bash install.sh
```

`install.sh` copies the binary and manifest to `/var/lib/coolercontrol/plugins/aquapx/`, and on
SELinux systems (Bazzite, Fedora Atomic, …) labels the binary `bin_t`. Without that label systemd
(`init_t`) is denied `execute` on files labelled `var_lib_t` and the plugin never starts. The
manifest's `executable` must be an absolute path.

## Device access

A udev rule grants access to the Aquacomputer HID nodes without root:

```
# /etc/udev/rules.d/99-aquacomputer.rules
SUBSYSTEM=="hidraw", ATTRS{idVendor}=="0c70", GROUP="input", MODE="0660", TAG+="uaccess"
```

## Done

- **D5 Next** (`0c70:f00e`): solid colour + all 23 onboard effects, CLI + CoolerControl plugin.
  - Decoded from an aquasuite USB capture stepping through every effect
    ([`internal/aqua/testdata/d5next_effects.txt`](internal/aqua/testdata/d5next_effects.txt)).
    The effect block is bytes 663–729 of report `0x03`, with the same layout as the LEAKSHIELD's
    577–643 block. The 15 shared effects are byte-identical, so both devices use one table.
    aquapx replays aquasuite's whole block for the chosen effect, so the sub-parameter and
    palette always match the effect, and puts the chosen colour in its main colour slot.
  - **In CoolerControl**: Rotating Rainbow, Swiping Rainbow, Single Colour, Breathing, Colour
    Shift, Colour Change, Blinking, Colour Sequence, Sequence, Scanner, Laser, Wave, Flame, Rain,
    Snowfall (`0x10`), Stardust (`0x11`), Colour Switch (`0x12`) and Bar Graph (`0x0d`).
  - **CLI only**: Sound Bars (`0x15`), Sound Flash (`0x14`), Sound Slider (`0x16`), Sound Shift
    (`0x17`) and AMBIENTpx (`0x18`). aquasuite drives these with audio or screen data streamed
    from the PC, which aquapx doesn't send, so they won't animate.
  - Not adjustable yet: effect speed/direction, the sensor source and range for Colour Switch
    and Bar Graph, and extra palette colours (these stay at aquasuite's defaults).
  - **Brightness**: CoolerControl's lighting protocol has no brightness setting, so the D5
    Next gets a "Pump RGB Brightness" 0–100% channel. It shows up like a fan channel; set it
    to a fixed value. It drives the master brightness byte (237). Effects never change it: a
    picked colour's brightness goes in the effect's colour slot instead. CLI:
    `aquapx d5 brightness [0-100]`. The strip doesn't have a slider yet: no strip brightness
    byte showed up in the captures.
  - **RGB output (strip)**: the accessories on the D5 Next's RGB output header are a second
    lighting channel, "Strip RGB", on the D5 Next in CoolerControl. It has the same effects and
    menu, minus System Status. Decoded from a capture of aquasuite stepping through all 23
    effects on the strip
    ([`internal/aqua/testdata/d5strip_effects.txt`](internal/aqua/testdata/d5strip_effects.txt)):
    its effect block is bytes 243–309, with the same layout and values as the pump's (only
    Sound Bars' byte 4 differs, 0x0e vs 0x14). Nothing else in the report changed, so the strip's
    brightness goes in its colour slot, and "Off" is a solid colour at brightness 0. CLI:
    `aquapx d5 strip color|effect|off|dump`.
- **High Flow Next** (`0c70:f012`): colour control, CLI + CoolerControl plugin. Reverse-engineered
  live on hardware (no captures exist for it). Its config is report `0x03`, 682 bytes, same
  CRC-16/USB scheme, laid out as eight ~70-byte LED-group blocks. Each block's colour window
  holds hue slots in **two layers** — a base/background colour plus the effect palette
  (`[hue16][0xffff]` entries) — and both must be written or you get e.g. a blue spinner over
  red LEDs. Only hue is decoded, so the device keeps running its own flow-indicator effect
  (whose speed tracks flow rate) in the chosen colour. Sensors verified unaffected.
- **LEAKSHIELD** (`0c70:f014`): colour (hue/saturation/brightness) + all 16 onboard effects,
  CLI + CoolerControl plugin.
  Decoded from aquasuite USB captures ([`re/decode_leakshield.py`](re/decode_leakshield.py)).
  Its config is report `0x03`, 750 bytes, on HID interface 1 (the device has two HID
  interfaces), same CRC-16/USB scheme, with a single HSV record at 620–623
  (`[hue16 BE][saturation][brightness]`, hue on the same 1536/360° scale). The save report
  `0x02` is sent as an **output** report, not a feature report. Verified on hardware: the live
  read matches the captured layout and CoolerControl sets the colour.
  - **Effects** (verified on hardware through the plugin): Rotating Rainbow, Swiping Rainbow,
    Single Colour, Breathing, Colour Shift, Colour Change, Blinking, Colour Sequence, Sequence,
    Scanner, Laser, Wave, Flame, Colour Switch, Bar Graph and Colour Gradient. The effect
    selector is byte 577 and uses the same values as the D5 Next; Colour Switch (`0x12`), Bar
    Graph (`0x0d`) and Colour Gradient (`0x21`) are LEAKSHIELD-only.
  - Each effect has its own parameter block and colour slots in bytes 577–643, which aquasuite
    rewrites on every change, so aquapx replays aquasuite's captured block for the effect and
    puts the chosen colour in its main colour slot. Single-colour effects (Breathing, Scanner,
    etc.) take CoolerControl's colour; rainbow, flame and the palette effects use their own.
    Choosing Single Colour or Off switches back from any running effect.
  - Not adjustable yet: effect speed/direction, Bar Graph's data source and range, and the
    extra palette colours (these stay at aquasuite's defaults).
  - CLI: `aquapx ls effects` lists them, `aquapx ls effect <name> [colour]` sets one, and
    `aquapx ls probe` does a read-only dump of the config report.

## To Do

- **Farbwerk Hub**: control non-Aquacomputer RGB accessories connected to the Farbwerk RGB
  headers.
  
## System Status mode

Both devices offer a **System Status (match fans)** lighting mode that mirrors coolerlink's
iCUE fan effect, so the whole loop reads as one system: **white at idle, red when any GPU is
above 70%**. Select it on either device in the CoolerControl UI and it drives both.

It writes only on an idle↔load transition (a few writes an hour), and in this mode the devices
run with the save/commit report suppressed — the colour applies live but isn't persisted, so
status updates cause no EEPROM wear. Choosing any explicit colour/effect stops the loop and
restores normal persisted writes.

## Safety

Writes are always read-modify-write of only the decoded LED bytes, gated on the live config's
CRC self-check, so pump/fan configuration is never disturbed. Every write can be reverted.

## Licence

GPL-3.0 (derives from the OpenRGB-captured protocol and shares scaffolding with coolerlink).
