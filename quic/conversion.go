package quic

import "strconv"

func uint8ToString(input uint8) string {
	return strconv.FormatUint(uint64(input), 10)
}
