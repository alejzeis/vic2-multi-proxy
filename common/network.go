package common

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/gorilla/websocket"
	"net"
	"strconv"
	"sync"
)

const DefaultVic2GamePort uint16 = 1930

type GameDataContainer struct {
	// An identifier for the origin of this data packet, so the relay on the server or client can know where this came from
	Identifier uint64
	Data       []byte
}

func DecodeGameDataContainer(data []byte) GameDataContainer {
	return GameDataContainer{
		Identifier: binary.BigEndian.Uint64(data[0:8]),
		Data:       data[8:],
	}
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

// Represents a connection capable of sending full messages between each other
// This is an abstracted type of UDP/Websocket connections used by the relay code, primarily to allow mocks for testing purposes
type MessageConnection interface {
	// Reads a message, blocking
	ReadMessage() ([]byte, net.Addr, error)
	// Sends a message
	WriteMessage(data []byte) error
	// Sends a closing message and closes the connection. Some implementations (UDP) don't support sending a message, but will still close the connection
	CloseWithMessage(msg string) error
	// Closes the underlying socket
	Close() error
	// Determine if the connection has been closed or not
	IsClosed() bool
}

// UDP implementation of MessageConnection
type UDPMessageConnection struct {
	socket      *net.UDPConn
	peerAddress *net.UDPAddr

	isClosedMutex *sync.RWMutex
	closed        bool
}

func (connection *UDPMessageConnection) ReadMessage() ([]byte, net.Addr, error) {
	data := make([]byte, 2048)
	length, addr, err := connection.socket.ReadFromUDP(data)
	if err != nil {
		return []byte{}, nil, err
	} else {
		if connection.peerAddress == nil {
			connection.peerAddress = addr
		}
		return data[0:length], addr, nil
	}
}

func (connection *UDPMessageConnection) WriteMessage(data []byte) error {
	if connection.peerAddress != nil {
		_, err := connection.socket.WriteToUDP(data, connection.peerAddress)
		return err
	} else {
		return errors.New("peer hasn't been identified yet")
	}
}

func (connection *UDPMessageConnection) CloseWithMessage(msg string) error {
	// Message Not supported
	return connection.Close()
}

func (connection *UDPMessageConnection) Close() error {
	connection.isClosedMutex.Lock()
	defer connection.isClosedMutex.Unlock()

	if !connection.closed {
		connection.closed = true
		return connection.socket.Close()
	} else {
		return errors.New("connection already closed")
	}
}

func (connection *UDPMessageConnection) IsClosed() bool {
	connection.isClosedMutex.RLock()
	defer connection.isClosedMutex.RUnlock()

	return connection.closed
}

type WebsocketMessageConnection struct {
	socket *websocket.Conn
	closed bool

	isClosedMutex *sync.RWMutex
}

func (connection *WebsocketMessageConnection) ReadMessage() ([]byte, net.Addr, error) {
	_, data, err := connection.socket.ReadMessage()
	if err != nil && websocket.IsCloseError(err, websocket.CloseNormalClosure) {
		connection.closed = true
	}
	return data, connection.socket.RemoteAddr(), err
}

func (connection *WebsocketMessageConnection) WriteMessage(data []byte) error {
	return connection.socket.WriteMessage(websocket.BinaryMessage, data)
}

func (connection *WebsocketMessageConnection) CloseWithMessage(msg string) error {
	err := connection.socket.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, msg))
	if err != nil {
		return err
	} else {
		return connection.Close()
	}
}

func (connection *WebsocketMessageConnection) Close() error {
	connection.isClosedMutex.Lock()
	defer connection.isClosedMutex.Unlock()

	if !connection.closed {
		connection.closed = true
		return connection.socket.Close()
	} else {
		return errors.New("connection already closed")
	}
}

func (connection *WebsocketMessageConnection) IsClosed() bool {
	connection.isClosedMutex.RLock()
	defer connection.isClosedMutex.RUnlock()

	return connection.closed
}

// Represents a source for creating MessageConnections, for listening locally and also connecting to remote addresses
type MessageConnectionProvider interface {
	// Creates and returns a new MessageConnection to listen on a certain port on the local machine
	ObtainLocalListener(port uint) (MessageConnection, error)
	// Creates and returns a new MessageConnection that is connected to the specified address
	DialForConnection(address string) (MessageConnection, error)
}

// Implements MessageConnectionProvider by creating UDP sockets for ObtainLocalListener, and websocket connections for DialForConnection
type RelayMessageConnectionProvider struct {
}

func (provider *RelayMessageConnectionProvider) ObtainLocalListener(port uint) (MessageConnection, error) {
	bindAddr := "127.0.0.1:" + strconv.Itoa(int(port))
	loopbackAddr, err := net.ResolveUDPAddr("udp", bindAddr)
	if err != nil {
		return nil, err
	} else {
		listener, err := net.ListenUDP("udp", loopbackAddr)
		if err != nil {
			return nil, err
		} else {
			udpMsgCon := new(UDPMessageConnection)
			udpMsgCon.socket = listener
			udpMsgCon.isClosedMutex = new(sync.RWMutex)
			udpMsgCon.closed = false
			return udpMsgCon, nil
		}
	}
}

func (provider *RelayMessageConnectionProvider) DialForConnection(address string) (MessageConnection, error) {
	webConn, _, err := websocket.DefaultDialer.Dial(address, nil)
	if err != nil {
		return nil, err
	} else {
		wsMsgCon := new(WebsocketMessageConnection)
		wsMsgCon.socket = webConn
		wsMsgCon.isClosedMutex = new(sync.RWMutex)
		wsMsgCon.closed = false
		return wsMsgCon, nil
	}
}
