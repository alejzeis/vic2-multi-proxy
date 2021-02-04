package common

import (
	"bytes"
	"encoding/binary"
)

type GameDataContainer struct {
	// True if going from client -> server, false if server -> client
	ToServer bool
	Data     []byte
}

func (container *GameDataContainer) Encode() []byte {
	buf := bytes.Buffer{}
	buf.Grow(5 + len(container.Data))

	lengthBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(lengthBuf, uint32(len(container.Data)+1))
	buf.Write(lengthBuf)

	if container.ToServer {
		buf.WriteByte(1)
	} else {
		buf.WriteByte(0)
	}

	buf.Write(container.Data)

	return buf.Bytes()
}
