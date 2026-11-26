#!/usr/bin/env python3
"""LEAKSHIELD fixed-colour write via read-modify-write of HID feature report 0x03.

Offsets decoded 2026-11-25 from aquasuite USB captures (see PROTOCOL.md):
  report 0x03 = 750 bytes, hue u16 BE @620 (1536/360 deg), sat @622, brightness @623,
  CRC-16/USB over [1:748] big-endian @748, then the constant save report 0x02 — which
  aquasuite sends as an OUTPUT report (SET_REPORT wValue 0x0202), not a feature report.

The captures only contain WRITES, so the live GET_REPORT(0x03) layout is unconfirmed. This
script therefore always saves the raw read (leakshield_report03.bin) and prints a hexdump,
and refuses to write unless the read is exactly 750 bytes AND its CRC self-validates.
Dry-run unless --write. Optional colour: --color RRGGBB (default pure green).

Requires: pip install hidapi   (run as root or with a udev rule for 0c70:f014)
"""
import sys, hid

VID, PID, RID, CFG_LEN = 0x0c70, 0xf014, 0x03, 750
IFACE = 1            # config interface (LEAKSHIELD has two HID interfaces: 1 and 2)
PROBE_LEN = 1024     # oversized read so a longer-than-expected report is visible
OFF_HUE, OFF_SAT, OFF_BRIGHT, OFF_CRC = 620, 622, 623, 748
SAVE = bytes([0x02, 0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00, 0x00, 0x34, 0xc6])

def crc16(data, poly=0x8005, init=0xFFFF, refin=True, refout=True, xorout=0xFFFF):
    rb = lambda b: int(f"{b:08b}"[::-1], 2)
    crc = init
    for b in data:
        crc ^= (rb(b) if refin else b) << 8
        for _ in range(8):
            crc = ((crc << 1) ^ poly) & 0xffff if crc & 0x8000 else (crc << 1) & 0xffff
    if refout:
        crc = int(f"{crc:016b}"[::-1], 2)
    return crc ^ xorout

def rgb_to_hsv_dev(r, g, b):
    import colorsys
    h, s, v = colorsys.rgb_to_hsv(r / 255, g / 255, b / 255)
    return round(h * 1536) % 1536, round(s * 255), round(v * 255)

def hexdump(b):
    for i in range(0, len(b), 16):
        print(f"{i:04x}  {b[i:i+16].hex(' ')}")

def open_device():
    infos = hid.enumerate(VID, PID)
    if not infos:
        sys.exit(f"LEAKSHIELD {VID:04x}:{PID:04x} not found")
    for i in infos:
        print(f"  found interface {i.get('interface_number')}: {i['path']}")
    pick = next((i for i in infos if i.get("interface_number") == IFACE), infos[0])
    print(f"using interface {pick.get('interface_number')}")
    h = hid.device()
    h.open_path(pick["path"])
    return h

# colour to write (default: pure green, full sat/brightness)
rgb = (0x00, 0xff, 0x00)
if "--color" in sys.argv:
    s = sys.argv[sys.argv.index("--color") + 1].lstrip("#")
    rgb = (int(s[0:2], 16), int(s[2:4], 16), int(s[4:6], 16))

h = open_device()
cfg = bytearray(h.get_feature_report(RID, PROBE_LEN))
open("leakshield_report03.bin", "wb").write(bytes(cfg))
print(f"read {len(cfg)} bytes (expected {CFG_LEN}) -> saved leakshield_report03.bin")
hexdump(cfg)

if len(cfg) != CFG_LEN:
    print(f"ABORT: length {len(cfg)} != {CFG_LEN}: live layout differs from the captures — refusing to write.")
    if len(cfg) >= 4:
        calc = crc16(cfg[1:-2]); stored = (cfg[-2] << 8) | cfg[-1]
        print(f"(CRC over [1:{len(cfg)-2}]: calc={calc:04x} stored={stored:04x} -> {'OK' if calc == stored else 'MISMATCH'})")
    h.close(); sys.exit(1)

calc = crc16(cfg[1:OFF_CRC]); stored = (cfg[OFF_CRC] << 8) | cfg[OFF_CRC + 1]
print(f"CRC self-check: calc={calc:04x} stored={stored:04x} -> {'OK' if calc == stored else 'MISMATCH'}")
if calc != stored:
    print("ABORT: live framing/offsets NOT confirmed — refusing to write."); h.close(); sys.exit(1)

hue = (cfg[OFF_HUE] << 8) | cfg[OFF_HUE + 1]
print(f"current: hue={hue:04x} ({hue / 1536 * 360:.1f} deg) sat={cfg[OFF_SAT]:02x} bright={cfg[OFF_BRIGHT]:02x}")

open("leakshield_original_config.bin", "wb").write(bytes(cfg))
print("saved original -> leakshield_original_config.bin")

nh, ns, nv = rgb_to_hsv_dev(*rgb)
new = bytearray(cfg)
new[OFF_HUE] = nh >> 8; new[OFF_HUE + 1] = nh & 0xff; new[OFF_SAT] = ns; new[OFF_BRIGHT] = nv
c = crc16(new[1:OFF_CRC]); new[OFF_CRC] = (c >> 8) & 0xff; new[OFF_CRC + 1] = c & 0xff
print(f"new:     hue={nh:04x} ({nh / 1536 * 360:.1f} deg) sat={ns:02x} bright={nv:02x} crc={c:04x}")
# bytes changed (sanity: should be only our fields + crc)
changed = [i for i in range(len(cfg)) if cfg[i] != new[i]]
print(f"bytes changed: {changed}")

if "--write" in sys.argv:
    h.send_feature_report(bytes(new))
    h.write(SAVE)  # save (constant) — OUTPUT report, as captured (wValue 0x0202)
    print(f">>> WROTE #{rgb[0]:02x}{rgb[1]:02x}{rgb[2]:02x} + save. Look at the LEAKSHIELD.")
else:
    print("(dry-run — no write; pass --write to apply)")
h.close()
