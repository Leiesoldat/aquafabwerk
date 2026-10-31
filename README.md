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

- **D5 Next** (`0c70:f00e`): solid colour + all 14 onboard effects, CLI + CoolerControl plugin.
- **High Flow Next** (`0c70:f012`): colour control, CLI + CoolerControl plugin. Reverse-engineered
  live on hardware (no captures exist for it). Its config is report `0x03`, 682 bytes, same
  CRC-16/USB scheme, laid out as eight ~70-byte LED-group blocks. Each block's colour window
  holds hue slots in **two layers** — a base/background colour plus the effect palette
  (`[hue16][0xffff]` entries) — and both must be written or you get e.g. a blue spinner over
  red LEDs. Only hue is decoded, so the device keeps running its own flow-indicator effect
  (whose speed tracks flow rate) in the chosen colour. Sensors verified unaffected.

## To Do

- **Ultitube Reservoir**
- **Leakshield**
  
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
