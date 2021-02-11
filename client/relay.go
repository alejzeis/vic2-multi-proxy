package client

import (
	"github.com/gorilla/websocket"
	"github.com/jython234/vic2-multi-proxy/common"
	log "github.com/sirupsen/logrus"
	"net"
	"strings"
	"sync"
)

func createStartRelay() *gameRelay {
	relay := new(gameRelay)
	relay.mutex = &sync.Mutex{}
	relay.localSignalChannel = make(chan channelSignal)
	relay.remoteSignalChannel = make(chan channelSignal)

	go relay.relayDataLocalToRemote()

	return relay
}

type channelSignal uint8

const (
	SHUTDOWN channelSignal = iota
	DISCONNECTED_MATCHMAKING
	WEBSOCKET_READY
	BEGIN_FORWARDING
	STOP_FORWARDING
)

type gameRelay struct {
	mutex            *sync.Mutex
	serverAddress    string
	remoteConnection *websocket.Conn

	localProxies map[uint64]*virtualGameProxy

	remoteSignalChannel chan channelSignal
}

// Represents a "proxied" game connected to the local actual game
// This is either one of various clients (if the local game is hosting)
// Or a virtual "server" (if the local game is linked to a remote lobby)
type virtualGameProxy struct {
	identifier uint64
	socket     *net.UDPConn

	signalChannel chan channelSignal
}

func (relay *gameRelay) shutdown() {
	for _, proxy := range relay.localProxies {
		proxy.signalChannel <- SHUTDOWN
	}
	relay.remoteSignalChannel <- SHUTDOWN

	// Await on their threads to exit (should send a message telling us shutdown complete
	<-relay.localSignalChannel
	<-relay.remoteSignalChannel
}

// TODO: Need to make this spawnable per-socket for when hosting a lobby
// Make this part of virtualGameProxy
// When hosting, each fake "client" will have their own UDP socket and thread instance of this
func (relay *gameRelay) relayDataLocalToRemote() {
	relay.mutex.Lock()

	addr := "127.0.0.1:1630"
	loopbackAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		log.WithError(err).WithField("addr", addr).Error("Failed to resolve loopback address")
		panic(err)
	}

	listener, err := net.ListenUDP("udp", loopbackAddr)
	if err != nil {
		log.WithError(err).WithField("addr", addr).Error("Failed to start listening locally")
		panic(err)
	}
	relay.localSocket = listener
	relay.mutex.Unlock()

	log.Info("Started listening for local game data")
	defer log.Info("Stopped listening for local game data")

	forwarding := false
	wsReady := false
	for {
		buf := make([]byte, 2048)
		length, address, err := relay.localSocket.ReadFromUDP(buf)
		if err != nil {
			log.WithError(err).WithField("address", address.String()).Warn("Error while reading data from local listening socket")
		}

		select {
		case signal := <-relay.localSignalChannel:
			switch signal {
			case SHUTDOWN:
				relay.localSignalChannel <- SHUTDOWN // Re-send signal so the shutdown function knows we are terminated
				return                               // Exit function, stopping the thread
			case WEBSOCKET_READY:
				wsReady = true
			case DISCONNECTED_MATCHMAKING:
				wsReady = false
				forwarding = false
			case STOP_FORWARDING:
				forwarding = false
			case BEGIN_FORWARDING:
				forwarding = true
			}
		default:
			// We want to do a non-blocking receive
		}

		if forwarding && wsReady {
			container := common.GameDataContainer{
				Identifier: 0, // TODO: Figure out what to set this
				Data:       buf[0:(length - 1)],
			}

			err := relay.remoteConnection.WriteMessage(websocket.BinaryMessage, container.Encode())
			if err != nil {
				log.WithFields(log.Fields{
					"gameAddr":  address.String(),
					"relayAddr": relay.serverAddress,
					"length":    length,
				}).WithError(err).Warn("Failed to relay a packet from local game to relay server")
			}
		}
	}
}

func (relay *gameRelay) relayDataRemoteToLocal() {
	forwarding := false
	defer relay.remoteConnection.Close()

	for {
		select {
		case signal := <-relay.remoteSignalChannel:
			switch signal {
			case DISCONNECTED_MATCHMAKING:
				return // Exit function, stopping the thread
			case SHUTDOWN:
				relay.remoteSignalChannel <- SHUTDOWN // Re-send signal so the shutdown function knows we are terminated
				return                                // Exit function, stopping the thread
			case STOP_FORWARDING:
				forwarding = false
			case BEGIN_FORWARDING:
				forwarding = true
			}
			break
		default:
			// We want to do a non-blocking receive
			break
		}

		msgType, data, err := relay.remoteConnection.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err) {
				log.WithError(err).Warn("Remote websocket connection closed by server")
				relay.onDisconnectedFromServer()
			} else {
				log.WithError(err).Warn("Failed to read message from remote websocket connection")
			}
		} else if msgType == websocket.BinaryMessage && forwarding {
			// TODO: Send data to game address
			container := common.DecodeGameDataContainer(data)
			//relay.localSocket.WriteToUDP()
		}
	}
}

func (relay *gameRelay) onConnectedToServer(address string, auth string) bool {
	relay.mutex.Lock()
	defer relay.mutex.Unlock()

	if relay.remoteConnection != nil {
		return true // already connected to server
	}

	if strings.HasPrefix(address, "http://") {
		relay.serverAddress = strings.ReplaceAll(address, "http://", "ws://")
	} else if strings.HasPrefix(address, "https://") {
		relay.serverAddress = strings.ReplaceAll(address, "https://", "wss://")
	} else {
		log.WithField("address", address).Error("Invalid address")
	}

	connection, _, err := websocket.DefaultDialer.Dial(relay.serverAddress, nil)
	if err != nil {
		log.WithError(err).WithField("address", relay.serverAddress).Error("Failed to connect to relay server")
		return false
	}
	err = connection.WriteMessage(websocket.TextMessage, []byte(auth))
	if err != nil {
		log.WithError(err).WithField("address", relay.serverAddress).Error("Failed to send authentication string to relay server")
		_ = connection.Close()
		return false
	}

	relay.remoteConnection = connection
	relay.localSignalChannel <- WEBSOCKET_READY
	go relay.relayDataRemoteToLocal()
	return true
}

func (relay *gameRelay) onDisconnectedFromServer() {
	relay.mutex.Lock()
	defer relay.mutex.Unlock()

	relay.localSignalChannel <- DISCONNECTED_MATCHMAKING
	relay.remoteSignalChannel <- DISCONNECTED_MATCHMAKING
	relay.serverAddress = ""
}

func (relay *gameRelay) setForwarding(forwarding bool) {
	if forwarding {
		relay.localSignalChannel <- BEGIN_FORWARDING
		relay.remoteSignalChannel <- BEGIN_FORWARDING
	} else {
		relay.localSignalChannel <- STOP_FORWARDING
		relay.remoteSignalChannel <- STOP_FORWARDING
	}
}
