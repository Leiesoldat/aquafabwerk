package aqua

// CRC16USB computes CRC-16/USB (poly 0x8005, init 0xFFFF, refin+refout, xorout 0xFFFF),
// using the reflected form (poly 0xA001). This is the checksum in the last two bytes of the
// Aquacomputer config reports; verified against the OpenRGB #2111 D5 Next captures.
func CRC16USB(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc ^ 0xFFFF
}
