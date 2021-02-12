package common

import (
	"bytes"
	"encoding/binary"
)

const DefaultVic2GamePort uint16 = 1930

func DecodeGameDataContainer(data []byte) GameDataContainer {
	return GameDataContainer{
		Identifier: binary.BigEndian.Uint64(data[0:8]),
		Data:       data[8:(len(data) - 1)],
	}
}

type GameDataContainer struct {
	// An identifier for the origin of this data packet, so the relay on the server or client can know where this came from
	Identifier uint64
	Data       []byte
}

func (container *GameDataContainer) Encode() []byte {
	buf := bytes.Buffer{}
	buf.Grow(8 + len(container.Data))

	identBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(identBuf, container.Identifier)
	buf.Write(identBuf)

	buf.Write(container.Data)

	return buf.Bytes()
}
