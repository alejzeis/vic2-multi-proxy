package common

import (
	"testing"
)

// The uint64 24 plus Some garbage data I just made up
const testContainerIdentifier = 24

var testContainerEncodedData = []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x18, 0x04, 0x00, 0xAE, 0xDD, 0xFE, 0x00, 0xA2, 0x5D, 0x7F, 0x2C, 0x4B, 0x4B}

func TestGameDataContainerEncode(t *testing.T) {
	container := GameDataContainer{
		Identifier: 24,
		Data:       testContainerEncodedData[8:],
	}

	for i, b := range container.Encode() {
		if b != testContainerEncodedData[i] {
			t.Log("Mismatch between expected encoded data and actual at index: ", i, " (", b, ", expected ", testContainerEncodedData[i], ")")
			t.Fail()
			break
		}
	}
}

func TestGameDataContainerDecode(t *testing.T) {
	container := DecodeGameDataContainer(testContainerEncodedData)
	if container.Identifier != testContainerIdentifier {
		t.Log("Mismatch between expected decoded Identifier (", container.Identifier, ", expected ", testContainerIdentifier, ")")
		t.Fail()
	} else {
		for i, b := range container.Data {
			if b != testContainerEncodedData[i+8] {
				t.Log("Mismatch between expected decoded container Data at index", i, "(", b, ", expected ", testContainerEncodedData[i+8], ")")
			}
		}
	}
}
