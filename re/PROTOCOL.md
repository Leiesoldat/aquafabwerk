# Aquacomputer D5 Next — RGB protocol (reverse-engineered 2026-08-10)

Decoded from the 21 USB captures in OpenRGB issue #2111 (`captures/`) using `decode.py`,
cross-checked against a live read of this rig's D5 Next. **No open-source project controls
Aquacomputer RGB yet — this is new.**

## Device / transport
- **D5 Next**: USB `0c70:f00e`. Two interfaces:
  - **IF0** vendor-specific (WinUSB), EP 0x02 OUT / 0x81 IN — *not* used for these RGB effects.
  - **IF1 HID**, EP 0x83 IN — sensors (kernel `aquacomputer_d5next`) + **feature-report config**.
- RGB is set via **HID SET_REPORT (feature) on report `0x03`** — hidraw, like coolerlink.
  Setup: `bmRequestType=0x21 bRequest=0x09 wValue=0x0303 wIndex=0x0001 wLength=0x0329`.
- Config report `0x03` = **809 bytes** (0x329), read/write. **Read-modify-write**: it also holds
  pump/fan curves — only touch the fields below.
- After writing `0x03`, aquasuite sends a **save report `0x02`** (11 bytes, own CRC) to persist.

## CRC (verified on 4 captures)
- **CRC-16/USB**: poly 0x8005, init 0xFFFF, refin+refout true, xorout 0xFFFF.
- Covers report bytes **[1:807]** (after the report-ID byte, excluding the 2 CRC bytes).
- Stored **big-endian** at bytes **807–808**.

## Field map (report 0x03, byte offsets)
| Offset | Field | Notes |
|---|---|---|
| 0 | report ID | 0x03 |
| **237** (0x0ed) | master **brightness** | 0x00=off … 0xff=full |
| **239** (0x0ef) | on/off flag | 0x02 seen in "Off" capture |
| **663** (0x297) | **effect selector** | 0x01=static/fixed, 0x02=breathing, 0x03=rainbow, … (15 modes captured) |
| 674–689 | effect params | speed / direction / etc. (per-mode) |
| **706–707** (0x2c2) | **hue** (uint16 BE) | scale ≈ 1536/360°: red=0x0000, green=0x01ff(≈120°), blue=0x03ff(≈240°) |
| **708** (0x2c4) | **saturation** | 0xff=full colour, 0x00=white |
| 807–808 (0x327) | CRC-16/USB | see above |

Colour model = **HSV**: hue16@706, sat@708, value=brightness@237. To set an arbitrary RGB:
convert RGB→HSV, write those three fields + effect=0x01 (static), recompute CRC, SET report 0x03, save report 0x02.

## Write procedure (Phase 1 — NOT YET DONE, requires a write)
1. hidraw `GET_FEATURE(0x03)` → 809 bytes (live baseline).
2. Patch brightness/effect/hue/sat only.
3. CRC-16/USB over [1:807] → big-endian @807–808.
4. hidraw `SET_FEATURE(0x03)`.
5. hidraw `SET_FEATURE(0x02)` save report (decode its 11-byte layout first).

## Open items
- Decode the save report `0x02` (11 bytes: `02 00 00 00 02 00 00 00 00 <crc16>`) — its CRC coverage.
- Per-LED direct addressing (64 LEDs): these effect captures store a single HSV, not a per-LED
  array — direct/AMBIENTpx likely uses the vendor IF0 (EP 0x02). Out of scope for v1 (fixed colour + onboard effects).
- Effect param semantics at 674–689 (speed/direction) for animated modes.
- **High Flow Next** (`0c70:f012`): separate device, own report layout — repeat this method.

## Toolkit
- `decode.py` — pcapng + USBPcap parser, extracts host→device payloads.
- `captures/` — 21 mode captures from OpenRGB #2111.
- Live baseline: `python3 -c "import hid;h=hid.device();h.open(0x0c70,0xf00e);print(bytes(h.get_feature_report(3,0x32a)).hex())"`

---

# High Flow Next (`0c70:f012`) — RGB (reverse-engineered live, 2026-08-11)

No captures exist for this device (OpenRGB #2111 is D5 Next only); decoded on hardware by
probing candidate fields and observing the LEDs.

- Config = **HID feature report `0x03`, 682 bytes**, same **CRC-16/USB over [1:680]**, big-endian
  at 680–681. Same read-modify-write + save-report(`0x02`) procedure as the D5 Next.
- Layout = **eight ~70-byte LED-group blocks** at offsets
  **98, 168, 238, 308, 378, 448, 518, 588**.
- Within each block, the **colour window starts at +40** and holds **six 4-byte records**
  (stride 4): **`[hue uint16 BE][saturation][value]`**. Hue is on the same **1536/360°** scale as
  the D5 Next. (Do NOT walk this window at stride 2 guessing hues by value — that writes hues
  into the sat/val bytes.)
- **Saturation is required for white**: white is hue-less (sat 0). Writing hue alone renders
  "white" as hue 0 = RED.
- **Value (brightness) must be PRESERVED, not overwritten**: the rotating spinner is rendered by
  brightness CONTRAST between records (dim background entries ~0x03-0x3c vs bright 0xff
  highlights). Flattening every record to one value makes the indicator a uniform block and the
  spinner vanishes. aquapx scales each record's own value instead.
- **TWO colour layers** (this is the gotcha): the effect **palette** entries are 4-byte
  `[hue16][0xffff]` pairs (mostly at +44,+48,+52,+56,+60), and a separate **base/background**
  colour sits in the earlier slots (+40/+42) which are *not* marker-terminated. Writing only the
  palette gives a recoloured spinner over base-coloured LEDs (observed: blue spinner, red LEDs).
  aquapx therefore treats a slot as a hue if it is marker-terminated **or** currently holds a
  value < 1536 — 51 slots on this unit.
- Hue, saturation and per-record brightness are decoded; effect-selection is not, so the device keeps
  running its onboard flow-indicator effect (speed tracks flow) in the chosen colour.
- **Sensors are unaffected** by these writes (verified: flow, water temp, quality all sane after).
- Restore baseline: `re/`-saved `hfn_original_config.bin` (write it back + save report).

## Concurrency (learned the hard way)

hidapi handles are **not safe for concurrent feature-report access**. Two goroutines doing
overlapping `GET_FEATURE` on one handle made both devices return `EPIPE` ("Broken pipe") and
stay wedged (sensors kept working via the kernel driver, but every feature report failed) until
a **USBDEVFS_RESET + ~25s settle** cleared it. aquapx serialises whole read-modify-write cycles
behind a per-device mutex, and the plugin runs a single status loop for both devices.
