package pack

import (
	"encoding/binary"
	"encoding/hex"
)

func Pack(funcTypeHex string, dataPayload []byte, uid uint64) []byte {

	// Convert funcTypeHex to bytes
	funcType, err := hex.DecodeString(funcTypeHex)
	if err != nil {
		return nil
	}
	if len(funcType) != 4 {
		return nil
	}

	// Create a header of 18 bytes
	header := make([]byte, 18)

	// [0-1] = 0x71AB
	header[0] = 0x71
	header[1] = 0xAB

	// [2-3] = 0x0000
	header[2] = 0x00
	header[3] = 0x00

	// [4-5] = total length (header + payload), big endian
	totalLength := len(dataPayload) + 18
	header[4] = byte((totalLength >> 8) & 0xFF)
	header[5] = byte(totalLength & 0xFF)

	// [6-9] = funcType (4 bytes)
	copy(header[6:10], funcType)

	// [10-15] = UID as big-endian 6-byte value
	// Write full 8-byte uint64, then take the last 6 bytes
	uidBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(uidBytes, uid)
	copy(header[10:], uidBytes) // Take last 6 bytes

	// Combine header + data
	packet := append(header, dataPayload...)

	return packet
}

func Unpack(data []byte) (bool, string, []byte) {
	if len(data) < 18 {
		return false, "", nil
	}

	if !(data[0] == 0x71 && data[1] == 0xAB) {
		return false, "", nil
	}

	dataTypeHex := hex.EncodeToString(data[8:10])
	return true, dataTypeHex, data[18:]
}
