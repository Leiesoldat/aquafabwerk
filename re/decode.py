#!/usr/bin/env python3
"""Minimal pcapng + USBPcap parser for the Aquacomputer D5 Next RE.
Extracts host->device (OUT / control SET) USB payloads from a capture so the LED
config writes can be diffed across modes. No external deps."""
import sys, struct

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
    if len(pd) < 27:
        return None
    headerLen = struct.unpack_from('<H', pd, 0)[0]
    function  = struct.unpack_from('<H', pd, 14)[0]
    info      = pd[16]                       # bit0: 1=host->device(?) / direction context
    endpoint  = pd[21]
    transfer  = pd[22]                       # 0=ISO 1=INTR 2=CTRL 3=BULK
    dataLength = struct.unpack_from('<I', pd, 23)[0]
    payload   = pd[headerLen:headerLen + dataLength]
    return dict(ep=endpoint, transfer=transfer, dlen=dataLength,
                data=payload, func=function, info=info, hlen=headerLen)

TT = {0: 'ISO', 1: 'INTR', 2: 'CTRL', 3: 'BULK'}

def out_payloads(path):
    """Return list of (ep, transfer, bytes) for host->device transfers carrying data."""
    out = []
    for pd in parse_pcapng(path):
        u = parse_usbpcap(pd)
        if not u or u['dlen'] == 0 or not u['data']:
            continue
        is_in = bool(u['ep'] & 0x80)         # endpoint bit7 = IN
        if is_in:
            continue                          # we want host->device (OUT / control-out)
        out.append((u['ep'], u['transfer'], u['data']))
    return out

if __name__ == '__main__':
    for path in sys.argv[1:]:
        ps = out_payloads(path)
        # dedup identical payloads, keep order; show distinct OUT writes
        seen, uniq = set(), []
        for ep, tt, d in ps:
            k = (ep, tt, bytes(d))
            if k in seen:
                continue
            seen.add(k); uniq.append((ep, tt, d))
        print(f"\n### {path.split('/')[-1]}  ({len(ps)} OUT xfers, {len(uniq)} distinct)")
        for ep, tt, d in uniq:
            if len(d) < 8:
                continue
            print(f"  ep=0x{ep:02x} {TT.get(tt,tt):4} len={len(d):4}  {d[:48].hex()}")
