#!/usr/bin/env python3
"""D5 Next fixed-colour write via read-modify-write of HID feature report 0x03.
Safety: aborts unless the LIVE config's CRC self-validates (confirms framing/offsets).
Only touches decoded colour fields; pump/fan config preserved. Dry-run unless --write."""
import sys, hid

VID, PID, RID, CFG_LEN = 0x0c70, 0xf00e, 0x03, 809
OFF_BRIGHT, OFF_ONOFF, OFF_EFFECT, OFF_HUE, OFF_SAT, OFF_CRC = 237, 239, 663, 706, 708, 807

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

# green: hue 0x01ff (~120 deg), full sat, full brightness, static effect
HUE_HI, HUE_LO, SAT, BRIGHT = 0x01, 0xff, 0xff, 0xff

h = hid.device(); h.open(VID, PID)
cfg = bytearray(h.get_feature_report(RID, CFG_LEN + 1))
print(f"read {len(cfg)} bytes")
calc = crc16(cfg[1:OFF_CRC]); stored = (cfg[OFF_CRC] << 8) | cfg[OFF_CRC + 1]
print(f"CRC self-check: calc={calc:04x} stored={stored:04x} -> {'OK' if calc==stored else 'MISMATCH'}")
if calc != stored:
    print("ABORT: live framing/offsets NOT confirmed — refusing to write."); h.close(); sys.exit(1)

print(f"current: bright={cfg[OFF_BRIGHT]:02x} onoff={cfg[OFF_ONOFF]:02x} "
      f"effect={cfg[OFF_EFFECT]:02x} hue={cfg[OFF_HUE]:02x}{cfg[OFF_HUE+1]:02x} sat={cfg[OFF_SAT]:02x}")

open("d5next_original_config.bin", "wb").write(bytes(cfg))
print("saved original -> d5next_original_config.bin")

new = bytearray(cfg)
new[OFF_EFFECT] = 0x01; new[OFF_ONOFF] = 0x00
new[OFF_HUE] = HUE_HI; new[OFF_HUE + 1] = HUE_LO; new[OFF_SAT] = SAT; new[OFF_BRIGHT] = BRIGHT
c = crc16(new[1:OFF_CRC]); new[OFF_CRC] = (c >> 8) & 0xff; new[OFF_CRC + 1] = c & 0xff
print(f"new:     bright={new[OFF_BRIGHT]:02x} onoff={new[OFF_ONOFF]:02x} "
      f"effect={new[OFF_EFFECT]:02x} hue={new[OFF_HUE]:02x}{new[OFF_HUE+1]:02x} sat={new[OFF_SAT]:02x} crc={c:04x}")
# bytes changed (sanity: should be only our fields + crc)
changed = [i for i in range(len(cfg)) if cfg[i] != new[i]]
print(f"bytes changed: {changed}")

if "--write" in sys.argv:
    h.send_feature_report(bytes(new))
    h.send_feature_report(bytes([0x02,0x00,0x00,0x00,0x02,0x00,0x00,0x00,0x00,0x34,0xc6]))  # save (constant)
    print(">>> WROTE green + save. Look at the pump ring.")
else:
    print("(dry-run — no write; pass --write to apply)")
h.close()
