package common

import (
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestGameDataContainer(t *testing.T) {
	suite.Run(t, new(GameDataContainerTestSuite))
}

// Ensures DialForConnection returns an error when I give it a bad address
func TestRelayMessageConnectionProvider_DialForConnection(t *testing.T) {
	provider := new(RelayMessageConnectionProvider)

	_, err := provider.DialForConnection("fakeaddress")
	assert.Error(t, err, "Providing an invalid address to DialForConnection should return an error")
}

func TestUDPMessageConnection(t *testing.T) {
	suite.Run(t, new(UDPMessageConnectionTestSuite))
}

func TestWebsocketMessageConnection(t *testing.T) {
	suite.Run(t, new(WebsocketMessageConnectionTestSuite))
}

// Test suite for GameDataContainer
type GameDataContainerTestSuite struct {
	suite.Suite

	testContainerIdentifier  uint64
	testContainerEncodedData []byte
}

func (ts *GameDataContainerTestSuite) SetupSuite() {
	// The uint64 24 plus Some garbage data I just made up
	ts.testContainerIdentifier = 24
	ts.testContainerEncodedData = []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x18, 0x04, 0x00, 0xAE, 0xDD, 0xFE, 0x00, 0xA2, 0x5D, 0x7F, 0x2C, 0x4B, 0x4B}
}

// Tests the encoding capability of a GameDataContainer by comparing it to a pre-encoded version
func (ts *GameDataContainerTestSuite) TestEncode() {
	container := GameDataContainer{
		Identifier: 24,
		Data:       ts.testContainerEncodedData[8:],
	}

	assert.Equal(ts.T(), ts.testContainerEncodedData, container.Encode(), "Encoded container must match the expected encoded result")
}

// Tests the decoding capability of a GameDataContainer by comparing the decoded parameters to already-known values
func (ts *GameDataContainerTestSuite) TestDecode() {
	container := DecodeGameDataContainer(ts.testContainerEncodedData)

	assert.Equal(ts.T(), ts.testContainerIdentifier, container.Identifier, "Container's identifier must match the expected identifier from the provided data")
	assert.Equal(ts.T(), ts.testContainerEncodedData[8:], container.Data, "Container's data must match the original encoded data after the first 8 bytes")
}

// Test Suite for UDPMessageConnection
type UDPMessageConnectionTestSuite struct {
	suite.Suite

	testEchoListenerPort uint
	testEchoUDPWriteData []byte
	testEchoUDPReadData  []byte
}

func (ts *UDPMessageConnectionTestSuite) SetupSuite() {
	ts.testEchoUDPWriteData = []byte{0xFD, 0x56, 0xDE, 0x21, 0x22, 0x07, 0x00, 0x00, 0x00, 0xAD, 0xCA, 0x75}
	ts.testEchoUDPReadData = []byte{0x00, 0x56, 0xFF, 0xDE, 0xAB, 0x21, 0x56}

	ts.testEchoListenerPort = 26180
}

// Used in TestUDPMessageConnectionReadWrite test function, takes a UDPMessageConnection and reads a message,
// checks to make sure that it matches what was sent, then sends a reply which is checked in the test function.
func (ts *UDPMessageConnectionTestSuite) echoUDPServer(conn MessageConnection, wg *sync.WaitGroup) {
	defer wg.Done()

	data, _, err := conn.ReadMessage()
	require.NoError(ts.T(), err, "ReadMessage from UDPMessageConnection should not return error while reading data from test UDP client socket")

	assert.Equal(ts.T(), len(ts.testEchoUDPReadData), len(data), "Length of packet read from UDPMessageConnection must match length of packet sent by UDP Client")
	assert.Equal(ts.T(), ts.testEchoUDPReadData, data, "Data of packet read from UDPMessageConnection must match what was sent by UDP client")

	err = conn.WriteMessage(ts.testEchoUDPWriteData)
	assert.NoError(ts.T(), err, "Writing reply message should not fail")
}

// Tests Reading and Writing of a UDPMessageConnection with a local udp socket connecting to it.
func (ts *UDPMessageConnectionTestSuite) TestReadWrite() {
	provider := RelayMessageConnectionProvider{}
	wg := new(sync.WaitGroup)

	conn, err := provider.ObtainLocalListener(ts.testEchoListenerPort)
	require.NoError(ts.T(), err, "ObtainLocalListener should not return error with valid port")

	defer func() {
		wg.Wait() // Wait for echoUDPServer goroutine to finish before closing the connection

		err := conn.Close()
		assert.NoError(ts.T(), err, "Closing socket on exit of test function should not return error")
		assert.True(ts.T(), conn.IsClosed(), "UDPMessageConnection should acknowledge it is closed")
	}()

	wg.Add(1)
	go ts.echoUDPServer(conn, wg)

	clientConn, err := net.Dial("udp", "127.0.0.1:"+strconv.Itoa(int(ts.testEchoListenerPort)))
	require.NoError(ts.T(), err, "Dialing local listener address should not return error")

	_, err = clientConn.Write(ts.testEchoUDPReadData)
	require.NoError(ts.T(), err, "Writing test data through UDP client socket should not return error")

	buffer := make([]byte, 1024)
	length, err := clientConn.Read(buffer)

	require.NoError(ts.T(), err, "Reading test data response through UDP client socket should not return error")
	assert.Equal(ts.T(), len(ts.testEchoUDPWriteData), length, "Length of packet written from UDPMessageConnection must match length of packet read by UDP Client")
	assert.Equal(ts.T(), ts.testEchoUDPWriteData, buffer[0:length], "Data of packet written from UDPMessageConnection must match what was read by UDP client")
}

type WebsocketMessageConnectionTestSuite struct {
	suite.Suite

	wsUpgrader *websocket.Upgrader

	testEchoListenerPort uint
	testWriteData        []byte
	testReadData         []byte
}

// Initializes the echo Websocket server which is used for the tests
func (ts *WebsocketMessageConnectionTestSuite) SetupSuite() {
	ts.testEchoListenerPort = 8089
	ts.testWriteData = []byte{0x00, 0x00, 0x00, 0xFE, 0xFD, 0xAB, 0xBD, 0x01, 0x04, 0x07}
	ts.testReadData = []byte{0x07, 0xAD, 0xFE, 0xD6, 0xA5, 0x21, 0x46, 0x87, 0x76, 0x64, 0xAD}

	ts.wsUpgrader = new(websocket.Upgrader)
	ts.wsUpgrader.ReadBufferSize = 2048
	ts.wsUpgrader.WriteBufferSize = 2048

	router := mux.NewRouter().StrictSlash(true)
	router.HandleFunc("/ws", ts.echoWSServer)

	go func() {
		err := http.ListenAndServe(":"+strconv.Itoa(int(ts.testEchoListenerPort)), router)
		require.NoError(ts.T(), err, "Starting Websocket HTTP server shouldn't return error")
	}()
}

func (ts *WebsocketMessageConnectionTestSuite) echoWSServer(w http.ResponseWriter, r *http.Request) {
	ws, err := ts.wsUpgrader.Upgrade(w, r, nil)
	require.NoError(ts.T(), err, "Upgrading connection to websocket should not return error")

	msgtype, data, err := ws.ReadMessage()
	require.NoError(ts.T(), err, "Reading message from websocket on server side shouldn't fail")
	assert.Equal(ts.T(), websocket.BinaryMessage, msgtype, "First message type received should be BinaryMessage")
	assert.Equal(ts.T(), ts.testWriteData, data, "Written data that was received by Websocket server should match the original written data")

	err = ws.WriteMessage(websocket.BinaryMessage, ts.testReadData)
	require.NoError(ts.T(), err, "Writing message from websocket on server side shouldn't fail")

	msgtype, data, err = ws.ReadMessage()
	assert.Error(ts.T(), err, "Reading second message from websocket on server-side should error out due to connection closing")
	assert.True(ts.T(), websocket.IsCloseError(err, websocket.CloseNormalClosure), "Error while reading message on server side should be because of normal CloseError")
}

func (ts *WebsocketMessageConnectionTestSuite) TestReadWrite() {
	time.Sleep(1 * time.Second) // Give some time for the server to start

	provider := new(RelayMessageConnectionProvider)

	conn, err := provider.DialForConnection("ws://127.0.0.1:" + strconv.Itoa(int(ts.testEchoListenerPort)) + "/ws")
	require.NoError(ts.T(), err, "Dialing for connection should not return error while server is running")

	err = conn.WriteMessage(ts.testWriteData)
	assert.NoError(ts.T(), err, "Writing message on WebsocketMessageConnection should not fail")

	data, _, err := conn.ReadMessage()
	assert.NoError(ts.T(), err, "Reading message on WebsocketMessageConnection should not fail")
	assert.Equal(ts.T(), ts.testReadData, data, "Message read on WebsocketMessageConnection should match what was written by server")

	err = conn.CloseWithMessage("message")
	assert.NoError(ts.T(), err, "Closing connection with message should not fail")

	assert.True(ts.T(), conn.IsClosed(), "WebsocketMessageConnection should acknowledge it is closed")
}
