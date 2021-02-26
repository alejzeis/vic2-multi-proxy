package server

import "github.com/gorilla/websocket"

// Represents the relay server which handles relaying all the actual game packets from the various clients to each other
type relayServer interface {
	registerNewConnection(conn *websocket.Conn)
	onUserStartHosting(user User, lobby Lobby)
	onUserStopHosting(user User, lobby Lobby)
	onUserLinkLobby(user User, lobby Lobby)
	onUserUnlinkLobby(user User, lobby Lobby)
}

type relayServerImpl struct {
	connections map[uint64]proxyClient
}

// Represents one of the many clients connected to the relay server, they may be hosting a game, linked to a game or neither
type proxyClient struct {
	userId     uint64
	connection *websocket.Conn

	signalChannel chan bool
}

func (client *proxyClient) handleIncomingData() {

}

func (relay *relayServerImpl) registerNewConnection(conn *websocket.Conn) {

}

func (relay *relayServerImpl) onUserStartHosting(user User, lobby Lobby) {

}

func (relay *relayServerImpl) onUserStopHosting(user User, lobby Lobby) {

}

func (relay *relayServerImpl) onUserLinkLobby(user User, lobby Lobby) {

}

func (relay *relayServerImpl) onUserUnlinkLobby(user User, lobby Lobby) {

}
