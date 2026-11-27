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
| **663** (0x297) | **effect selector** | start of the 67-byte effect block [663:730), same layout and values as the LEAKSHIELD block at 577 (see below); 23 effects decoded 2026-11-27, table in `internal/aqua/d5next_effects.go` |
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

---

# LEAKSHIELD (`0c70:f014`) — RGB (reverse-engineered from captures, 2026-11-25)

Decoded from aquasuite USBPcap captures (Windows 11, aquasuite → LEAKSHIELD) by diffing
config writes that changed one colour setting at a time: red→blue, green (pure and 135°),
brightness full→50%→full, pastel green (50% saturation).

- Config = **HID feature report `0x03`, 750 bytes** (0x2ee), IF1, same **CRC-16/USB over [1:748]**,
  big-endian at 748–749. Setup: `21 09 03 03 01 00 ee 02`.
- Same write + save-report procedure as the D5 Next / High Flow Next. aquasuite sends report `0x03`
  then the save report `0x02` as an **OUTPUT report** (`21 09 02 02 01 00 0b 00`, wValue 0x0202 —
  *not* a feature report), which was **byte-identical in every capture**:
  `02 00 00 00 02 00 00 00 00 34 c6` (`34c6` = CRC-16/USB of bytes [1:9]) — safe to send as a constant.
- Every write in the captures was accepted (status 0) and every CRC validated.

| Offset | Field | Notes |
|---|---|---|
| 0 | report ID | 0x03 |
| **620–621** (0x26c) | **hue** (uint16 BE) | same **1536/360°** scale: red=0x0000, green=0x01ff (120°), blue=0x03ff (240°); 0x023f = 135° |
| **622** (0x26e) | **saturation** | 0xff=full colour, 0x7f=pastel (50%); 0x00=white (inferred) |
| **623** (0x26f) | **brightness** (value) | 0xff=full, 0x7f=50% |
| 748–749 (0x2ec) | CRC-16/USB | over [1:748] |

- Colour model = **HSV in one contiguous 4-byte record** `[hue16][sat][val]` — same record shape as
  the High Flow Next colour window, but a single colour and brightness is per-colour (not a separate
  master byte like the D5 Next @237).
- No other bytes changed across any colour/brightness/saturation capture.
- Live `GET_FEATURE(0x03)` **verified on hardware** (2026-11-26): returns the same 750-byte layout
  with a valid CRC, so read-modify-write works as on the other devices.

## LEAKSHIELD effects (decoded 2026-11-26)

From one aquasuite capture stepping through every effect in order (base colour red). Only
**[577:644]** changes between effects; every write's CRC validated.

- **Effect selector = byte 577 (0x241)**, with the **same values as the D5 Next** (@663) plus three
  LEAKSHIELD-only effects.
- 579 = effect sub-parameter (0x04 colour change, 0x06 blinking/bar graph, 0x07 colour gradient,
  else 0x00). 586–619 = per-effect parameters (speed/timing; not individually decoded).
- From **620**: a list of 4-byte HSV **colour slots** `[hue16 BE][sat][val]` (slot *k* at 620+4*k*).
  Single colour uses slot 0 (= the static colour record above). Several effects start with a
  dim background record (`00 00 00 0f`) in slot 0 and put the foreground colour in slot 1.
- aquasuite rewrites the whole [577:644] block on every effect change, so aquapx replays each
  effect's captured block and writes the base colour into its primary slot.

| aquasuite name | aquapx mode | Selector @577 | Base-colour slot |
|---|---|---|---|
| Rotating rainbow | `rainbow` | 0x03 | — (self-coloured) |
| Swiping rainbow | `swipingrainbow` | 0x13 | — |
| Single color | `static` | 0x01 | 0 |
| Breathing | `breathing` | 0x02 | 0 |
| Color shift | `colorshift` | 0x0c | 0 |
| Color change | `colorchange` | 0x05 | 0 (+ 2 more palette colours) |
| Blinking | `blinking` | 0x04 | 1 |
| Color sequence | `colorsequence` | 0x0b | 0 (+ 1 more) |
| Sequence | `sequence` | 0x07 | 1 (+ 1 more) |
| Scanner | `scanner` | 0x08 | 1 (+ 1 more) |
| Laser | `laser` | 0x09 | 1 (+ 1 more) |
| Wave | `wave` | 0x0a | 1 |
| Flame | `flame` | 0x0e | — (3 flame colours) |
| **Color switch** | `colorswitch` | **0x12** | — (6-colour palette) |
| **Bar graph** | `bargraph` | **0x0d** | — (5-colour palette; source/range params at 579+) |
| **Color gradient** | `colorgradient` | **0x21** | — (gradient stops) |

- Not yet decoded: the individual parameter fields (speed, direction, bar-graph data source and
  range), and editing the extra palette colours. Those need captures that change one at a time.

## Concurrency (learned the hard way)

hidapi handles are **not safe for concurrent feature-report access**. Two goroutines doing
overlapping `GET_FEATURE` on one handle made both devices return `EPIPE` ("Broken pipe") and
stay wedged (sensors kept working via the kernel driver, but every feature report failed) until
a **USBDEVFS_RESET + ~25s settle** cleared it. aquapx serialises whole read-modify-write cycles
behind a per-device mutex, and the plugin runs a single status loop for both devices.

## D5 Next effects (decoded 2026-11-27)

One aquasuite capture stepping through all 23 D5 Next effects (base colour red, every CRC
valid). Only **[663:730]** changes between effects. The block has the same layout as the
LEAKSHIELD's [577:644]: selector, sub-parameter at +2, parameters, then 4-byte HSV colour slots
from +43 (= 706, the hue field). The 15 effects both devices share are byte-identical.

D5 Next-only selectors: Rain `0x0f`, Snowfall `0x10`, Stardust `0x11` (dim background in slot 0,
drop/flake colour in slot 1), Sound flash `0x14`, Sound bars `0x15`, Sound slider `0x16`,
Sound shift `0x17`, AMBIENTpx `0x18`. The sound effects and AMBIENTpx animate from data
aquasuite streams from the PC, which isn't decoded yet.
