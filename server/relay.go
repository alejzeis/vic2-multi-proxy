package server

import (
	"github.com/alejzeis/vic2-multi-proxy/common"
	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
	"sync"
)

// Represents the relay server which handles relaying all the actual game packets from the various clients to each other
type RelayServer interface {
	initialize(jwtSecret []byte, matchmaker *Matchmaker)
	registerNewConnection(conn *websocket.Conn)
	onUserStartHosting(user User, lobby Lobby)
	onUserStopHosting(user User, lobby Lobby)
	onUserLinkLobby(user User, lobby Lobby)
	onUserUnlinkLobby(user User, lobby Lobby)
}

type RelayServerImpl struct {
	connections      map[uint64]*proxyClient
	connectionsMutex *sync.RWMutex
	jwtSecret        []byte
	matchmaker       *Matchmaker
}

// Represents one of the many clients connected to the relay server, they may be hosting a game, linked to a game or neither
type proxyClient struct {
	relay *RelayServerImpl

	userId       uint64
	forwardingTo uint64
	hosting      bool
	connection   *websocket.Conn

	authenticated bool

	forwardRoutineRunning bool

	sendChannel   chan common.GameDataContainer
	signalChannel chan proxyClientSignal
}

type proxyClientSignal struct {
	stop      bool
	forwardTo uint64
	hosting   bool
}

func (client *proxyClient) handleIncomingData() {
	defer func() {
		client.relay.connectionsMutex.Lock() // Lock-write to prevent trying to send data through our exiting client
		defer client.relay.connectionsMutex.Unlock()

		client.authenticated = false // Set to false so no more data can be forwarded
		client.forwardRoutineRunning = false
		// Close the send channel so the forwarding goroutine can terminate
		// If we didn't lock while doing this, another goroutine could send data after the channel closed, causing a panic
		close(client.sendChannel)

		err := client.connection.Close()
		if err != nil {
			log.WithField("userId", client.userId).WithError(err).Error("Failed to close client relay socket")
		}

		delete(client.relay.connections, client.userId) // Remove ourselves from the map

		log.WithField("userId", client.userId).Debug("Relay connection exited")
	}()

	for {
		msgType, data, err := client.connection.ReadMessage()
		if err != nil {
			log.WithError(err).WithFields(log.Fields{
				"userId":     client.userId,
				"remoteAddr": client.connection.RemoteAddr().String(),
				"msgType":    msgType,
			}).Error("Error while reading data from client relay connection")
			return
		} else {
			switch msgType {
			case websocket.CloseMessage:
				// Client closing connection
				log.WithError(err).WithFields(log.Fields{
					"userId":     client.userId,
					"remoteAddr": client.connection.RemoteAddr().String(),
				}).Info("Relay connection closed")
				return // Exit function
			case websocket.TextMessage: // Used for sending initial auth token string
				if !client.authenticated {
					authStr := string(data)
					valid, tokenClaims := verifyToken(authStr, client.relay.jwtSecret)
					if valid {
						client.authenticated = true
						client.userId = tokenClaims.UserID // Token is signed with secret, so we can trust the userID sent.
						log.WithFields(log.Fields{
							"username":   tokenClaims.Subject,
							"userId":     tokenClaims.UserID,
							"remoteAddr": client.connection.RemoteAddr().String(),
						}).Info("Relay connection authenticated")
					} else {
						log.WithField("remoteAddr", client.connection.RemoteAddr().String()).Warn("Failed authentication attempt for relay connection")
					}
				}
				break
			case websocket.BinaryMessage:
				container := common.DecodeGameDataContainer(data)
				client.relay.connectionsMutex.RLock()
				if client.forwardingTo > 0 {
					// This client is linked to a lobby, whose hosting user ID is stored in client.forwardingTo.
					// So we just send to the client with that id
					container.Identifier = client.userId // Set the container's ID to our userId so the client relay program knows where it came from
					client.relay.connections[client.forwardingTo].sendChannel <- container
				} else if client.hosting {
					// This client is hosting a lobby, so the container identifier will tell us what client to send the packet to
					clientToSendTo, found := client.relay.connections[container.Identifier]
					if found && clientToSendTo.authenticated { // Make sure the client is in the map, just in case the container ID is bogus
						clientToSendTo.sendChannel <- container
					}
				}
				client.relay.connectionsMutex.RUnlock()
				break
			default:
				log.WithFields(log.Fields{
					"userId":     client.userId,
					"remoteAddr": client.connection.RemoteAddr().String(),
					"msgType":    msgType,
				}).Debug("Unhandled message type received")
				break
			}
		}

		select {
		case signal := <-client.signalChannel:
			if signal.stop {
				return // Stop handling data, exit function
			} else {
				// Signal is telling us updated values
				client.forwardingTo = signal.forwardTo
				client.hosting = signal.hosting
			}
		default:
			// We want to do a non-blocking receive
			break
		}
	}
}

// Handles sending packets back to the client
func (client *proxyClient) forwardData() {
	for client.forwardRoutineRunning {
		container, notClosed := <-client.sendChannel
		if notClosed {
			err := client.connection.WriteMessage(websocket.BinaryMessage, container.Encode())
			if err != nil {
				log.WithError(err).WithFields(log.Fields{
					"userId":     client.userId,
					"remoteAddr": client.connection.RemoteAddr().String(),
				}).Warn("Error while writing data to relay connection")
			}
		}
	}
}

func (relay *RelayServerImpl) findClientAndSendSignal(userId uint64, signal proxyClientSignal) {
	client, found := relay.connections[userId]
	if found {
		client.signalChannel <- signal
	}
}

func (relay *RelayServerImpl) initialize(jwtSecret []byte, matchmaker *Matchmaker) {
	relay.jwtSecret = jwtSecret
	relay.matchmaker = matchmaker
	relay.connections = make(map[uint64]*proxyClient)
	relay.connectionsMutex = &sync.RWMutex{}
}

func (relay *RelayServerImpl) registerNewConnection(conn *websocket.Conn) {
	client := new(proxyClient)
	client.relay = relay
	client.authenticated = false
	client.forwardingTo = 0
	client.connection = conn
	client.signalChannel = make(chan proxyClientSignal)
	client.sendChannel = make(chan common.GameDataContainer)
	client.forwardRoutineRunning = true

	go client.handleIncomingData()
	go client.forwardData()
}

func (relay *RelayServerImpl) onUserStartHosting(user User, lobby Lobby) {
	relay.connectionsMutex.RLock()
	defer relay.connectionsMutex.RUnlock()

	relay.findClientAndSendSignal(user.ID, proxyClientSignal{
		stop:      false,
		forwardTo: 0,
		hosting:   true,
	})
}

func (relay *RelayServerImpl) onUserStopHosting(user User, lobby Lobby) {
	relay.connectionsMutex.RLock()
	defer relay.connectionsMutex.RUnlock()

	relay.findClientAndSendSignal(user.ID, proxyClientSignal{
		stop:      false,
		forwardTo: 0,
		hosting:   false,
	})
}

func (relay *RelayServerImpl) onUserLinkLobby(user User, lobby Lobby) {
	relay.connectionsMutex.RLock()
	defer relay.connectionsMutex.RUnlock()

	relay.findClientAndSendSignal(user.ID, proxyClientSignal{
		stop:      false,
		forwardTo: lobby.HostUserID,
		hosting:   false,
	})
}

func (relay *RelayServerImpl) onUserUnlinkLobby(user User, lobby Lobby) {
	relay.connectionsMutex.RLock()
	defer relay.connectionsMutex.RUnlock()

	relay.findClientAndSendSignal(user.ID, proxyClientSignal{
		stop:      false,
		forwardTo: 0,
		hosting:   false,
	})
}
