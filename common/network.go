package common

import (
	"bytes"
	"encoding/binary"
)

type GameDataContainer struct {
	Relay  bool
	Origin uint16
	Data   []byte
}

func (container *GameDataContainer) Encode() []byte {
	buf := bytes.Buffer{}
	buf.Grow(7 + len(container.Data))

	lengthBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lengthBuf, uint32(len(container.Data)+3))
	buf.Write(lengthBuf)

	if container.Relay {
		buf.WriteByte(1)
	} else {
		buf.WriteByte(0)
	}
	originBuf := make([]byte, 2)
	binary.LittleEndian.PutUint16(originBuf, container.Origin)
	buf.Write(originBuf)

	buf.Write(container.Data)

	return buf.Bytes()
}
