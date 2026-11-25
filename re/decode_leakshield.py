#!/usr/bin/env python3
"""pcapng + USBPcap decoder for the LEAKSHIELD (0c70:f014) RE.

Extracts the HID SET_REPORT writes aquasuite sends (FEATURE report 0x03 = config, OUTPUT
report 0x02 = save/commit), validates each one's CRC-16/USB, prints the decoded colour fields, and diffs each 0x03
write against the previous one so a capture that changes one setting shows exactly which bytes
moved. Also prints any GET_REPORT(0x03) responses if the capture contains them.
No external deps.

usage: decode_leakshield.py capture1.pcapng [capture2.pcapng ...]
       (writes are diffed across all files in order, so pass a baseline first)
"""
import sys, struct

LS_LEN, OFF_HUE, OFF_SAT, OFF_BRIGHT, OFF_CRC = 750, 620, 622, 623, 748

def parse_pcapng(path):
    data = open(path, 'rb').read()
    off, endian, pkts = 0, '<', []
    while off + 12 <= len(data):
        btype = struct.unpack_from('<I', data, off)[0]
        if btype == 0x0A0D0D0A:  # Section Header Block -> detect endianness
            magic = struct.unpack_from('<I', data, off + 8)[0]
            endian = '<' if magic == 0x1A2B3C4D else '>'
        blen = struct.unpack_from(endian + 'I', data, off + 4)[0]
        if blen < 12 or off + blen > len(data):
            break
        body = data[off + 8: off + blen - 4]
        if btype == 0x00000006 and len(body) >= 20:      # Enhanced Packet Block
            caplen = struct.unpack_from(endian + 'I', body, 12)[0]
            pkts.append(body[20:20 + caplen])
        elif btype == 0x00000003 and len(body) >= 4:     # Simple Packet Block
            pkts.append(body[4:])
        off += blen
    return pkts

def parse_usbpcap(pd):
    """USBPcap packet header (little-endian):
    0 headerLen u16 | 2 irpId u64 | 10 status u32 | 14 function u16 | 16 info u8 (bit0: 1 = completion,
    device->host) | 17 bus u16 | 19 device u16 | 21 endpoint u8 | 22 transfer u8 | 23 dataLength u32 |
    27 control stage u8 (control transfers only: 0 setup, 1 data, 2 status, 3 complete)."""
    if len(pd) < 27:
        return None
    hl = struct.unpack_from('<H', pd, 0)[0]
    status = struct.unpack_from('<I', pd, 10)[0]
    info, bus, dev, ep, tt, dlen = struct.unpack_from('<BHHBBI', pd, 16)
    stage = pd[27] if tt == 2 and hl > 27 else None
    return dict(hlen=hl, status=status, info=info, bus=bus, dev=dev, ep=ep, tt=tt,
                dlen=dlen, stage=stage, irp=pd[2:10], data=pd[hl:hl + dlen])

def crc16usb(data):
    c = 0xFFFF
    for b in data:
        c ^= b
        for _ in range(8):
            c = (c >> 1) ^ 0xA001 if c & 1 else c >> 1
    return c ^ 0xFFFF

def crc_ok(r):
    return len(r) >= 4 and crc16usb(r[1:-2]) == (r[-2] << 8 | r[-1])

def colour(r):
    hue = r[OFF_HUE] << 8 | r[OFF_HUE + 1]
    return f"hue=0x{hue:04x} ({hue / 1536 * 360:5.1f} deg) sat={r[OFF_SAT]:3d} bright={r[OFF_BRIGHT]:3d}"

def events(path):
    """Yield ('set', type, rid, report, u) for SET_REPORT writes (type 2 = output, 3 = feature)
    and ('get', 3, rid, report, u) for GET_REPORT feature responses (matched by IRP id)."""
    pending_get = {}
    for pd in parse_pcapng(path):
        u = parse_usbpcap(pd)
        if not u or u['tt'] != 2:
            continue
        d = u['data']
        if not (u['info'] & 1) and len(d) >= 8:                  # host->device setup
            bm, req, wval = d[0], d[1], struct.unpack_from('<H', d, 2)[0]
            rtype, rid = wval >> 8, wval & 0xff
            if bm == 0x21 and req == 0x09 and rtype in (2, 3):   # SET_REPORT output/feature
                yield ('set', rtype, rid, d[8:], u)
            elif bm == 0xA1 and req == 0x01 and rtype == 3:      # GET_REPORT feature
                pending_get[u['irp']] = rid
        elif (u['info'] & 1) and u['irp'] in pending_get and d:  # completion with data
            yield ('get', 3, pending_get.pop(u['irp']), d, u)

if __name__ == '__main__':
    prev = None
    for path in sys.argv[1:]:
        print(f"\n### {path.split('/')[-1]}")
        for kind, rtype, rid, r, u in events(path):
            tag = f"bus {u['bus']} dev {u['dev']}"
            kind = f"{kind.upper()} {'OUT' if rtype == 2 else 'FEAT'}"
            ok = 'CRC OK' if crc_ok(r) else 'CRC MISMATCH'
            if rid == 0x03 and len(r) == LS_LEN:
                print(f"  {kind} 0x03 {len(r)}B {ok}  {colour(r)}  [{tag}]")
                if kind.startswith('SET'):
                    if prev is not None:
                        diff = [i for i in range(LS_LEN) if r[i] != prev[i] and i < OFF_CRC]
                        print("      changed vs previous write: " +
                              (', '.join(f"{i}(0x{i:03x}) {prev[i]:02x}->{r[i]:02x}" for i in diff) or 'none'))
                    prev = r
            else:
                print(f"  {kind} 0x{rid:02x} {len(r)}B {ok}  {r[:24].hex(' ')}{' ...' if len(r) > 24 else ''}  [{tag}]")
