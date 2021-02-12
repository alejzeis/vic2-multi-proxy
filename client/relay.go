package client

import (
	"github.com/alejzeis/vic2-multi-proxy/common"
	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
	"net"
	"strconv"
	"strings"
	"sync"
)

func createStartRelay() *gameRelay {
	relay := new(gameRelay)
	relay.mutex = new(sync.Mutex)
	relay.localProxies = make(map[uint64]*virtualGameProxy)
	relay.remoteSignalChannel = make(chan channelSignal)

	return relay
}

type channelSignal uint8

const (
	SHUTDOWN channelSignal = iota
	DISCONNECTED_MATCHMAKING
	BEGIN_HOSTING_FORWARDING
	BEGIN_LINKED_FORWARDING
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

	relay *gameRelay

	signalChannel chan channelSignal
}

// When hosting, each fake "client" will have their own UDP socket and thread instance of this
func (proxy *virtualGameProxy) relayDataLocalToRemote(port uint16) {
	bindAddr := "127.0.0.1:" + strconv.Itoa(int(port))
	loopbackAddr, err := net.ResolveUDPAddr("udp", bindAddr)
	if err != nil {
		log.WithError(err).WithField("addr", bindAddr).Error("Failed to resolve loopback address")
		panic(err)
	}

	listener, err := net.ListenUDP("udp", loopbackAddr)
	if err != nil {
		log.WithError(err).WithField("addr", bindAddr).Error("Failed to start listening locally")
		panic(err)
	}
	proxy.socket = listener

	log.WithFields(log.Fields{
		"address":         bindAddr,
		"proxyIdentifier": proxy.identifier,
	}).Debug("Started listening for local game data")

	defer func() {
		err := proxy.socket.Close()
		if err != nil {
			log.WithError(err).WithFields(log.Fields{
				"identifier":   proxy.identifier,
				"localAddress": bindAddr,
			}).Error("Failed to close local UDP socket for virtualGameProxy.")
		}

		log.WithFields(log.Fields{
			"address":         bindAddr,
			"proxyIdentifier": proxy.identifier,
		}).Debug("Stopped listening for local game data")
		proxy.signalChannel <- SHUTDOWN // Re-send signal so the shutdown function knows we are terminated
	}()

	for {
		buf := make([]byte, 2048)
		length, address, err := proxy.socket.ReadFromUDP(buf)
		if err != nil {
			log.WithError(err).WithFields(log.Fields{
				"localAddress":    bindAddr,
				"remoteAddress":   address,
				"proxyIdentifier": proxy.identifier,
			}).Warn("Error while reading data from local listening socket")
		}

		select {
		case signal := <-proxy.signalChannel:
			switch signal {
			case SHUTDOWN:
				return // Exit function, stopping the thread
			}
		default:
			// We want to do a non-blocking receive
		}

		container := common.GameDataContainer{
			Identifier: proxy.identifier,
			Data:       buf[0:(length - 1)],
		}

		err = proxy.relay.remoteConnection.WriteMessage(websocket.BinaryMessage, container.Encode())
		if err != nil {
			log.WithFields(log.Fields{
				"gameAddr":        address.String(),
				"relayAddr":       proxy.relay.serverAddress,
				"proxyIdentifier": proxy.identifier,
				"localAddress":    bindAddr,
				"length":          length,
			}).WithError(err).Warn("Failed to relay a packet from local game to relay server")
		}
	}
}

func (proxy *virtualGameProxy) sendGamePacket(container common.GameDataContainer) {
	// TODO: send container data to local game instance
}

func (relay *gameRelay) shutdown() {
	for _, proxy := range relay.localProxies {
		proxy.signalChannel <- SHUTDOWN
	}
	relay.remoteSignalChannel <- SHUTDOWN

	log.WithField("proxyCount", len(relay.localProxies)).Info("Sent shutdown signal to all proxy threads, waiting for exits...")
	// Await on their threads to exit (should send a message telling us shutdown complete
	<-relay.remoteSignalChannel
	for _, proxy := range relay.localProxies {
		<-proxy.signalChannel
	}
	log.Info("All proxy threads exited.")
}

func (relay *gameRelay) relayDataRemoteToLocal() {
	forwarding := false
	hosting := false
	defer func() {
		err := relay.remoteConnection.Close()
		if err != nil {
			log.WithError(err).WithField("address", relay.serverAddress).Error("Failed to close websocket connection to relay server.")
		}
		relay.remoteSignalChannel <- SHUTDOWN // Re-send signal so the shutdown function knows we are terminated
	}()

	for {
		select {
		case signal := <-relay.remoteSignalChannel:
			switch signal {
			case DISCONNECTED_MATCHMAKING:
				return // Exit function, stopping the thread
			case SHUTDOWN:
				return // Exit function, stopping the thread
			case STOP_FORWARDING:
				forwarding = false
			case BEGIN_HOSTING_FORWARDING:
				forwarding = true
				hosting = true
			case BEGIN_LINKED_FORWARDING:
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
			container := common.DecodeGameDataContainer(data)

			relay.mutex.Lock()

			proxy, found := relay.localProxies[container.Identifier]
			if found {
				proxy.sendGamePacket(container)
			} else if hosting {
				// Proxy instance wasn't found, so it must be a new connection.
				// Create a new virtualGameProxy to represent this connection
				proxy = &virtualGameProxy{
					identifier:    proxy.identifier,
					socket:        nil,
					relay:         relay,
					signalChannel: make(chan channelSignal),
				}
				relay.localProxies[proxy.identifier] = proxy
			}

			relay.mutex.Unlock()
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
	go relay.relayDataRemoteToLocal()
	return true
}

func (relay *gameRelay) onDisconnectedFromServer() {
	relay.mutex.Lock()
	defer relay.mutex.Unlock()

	for _, proxy := range relay.localProxies {
		proxy.signalChannel <- SHUTDOWN
		<-proxy.signalChannel // Wait for thread to exit
	}
	relay.remoteSignalChannel <- DISCONNECTED_MATCHMAKING
	relay.serverAddress = ""
	relay.localProxies = make(map[uint64]*virtualGameProxy) // Overwrite the map, we don't need the old proxies anymore
}

func (relay *gameRelay) onBeginHosting() {
	relay.mutex.Lock() // Lock mutex since we're reading the list of local proxies
	defer relay.mutex.Unlock()

	if len(relay.localProxies) < 1 { // There shouldn't be any virtualgameproxys on host start, or link start either
		// No need to create any virtualgameproxy instances,
		// the relayDataRemoteToLocal function will automatically create them as packets come in from Relay server
		relay.remoteSignalChannel <- BEGIN_HOSTING_FORWARDING // Start allowing forwarding data from Relay server to the local game instance
	}
}

func (relay *gameRelay) onStopHosting() {
	relay.mutex.Lock() // Lock mutex since we're reading the list of local proxies
	defer relay.mutex.Unlock()

	relay.remoteSignalChannel <- STOP_FORWARDING

	for _, proxy := range relay.localProxies {
		proxy.signalChannel <- SHUTDOWN
		<-proxy.signalChannel // Wait for thread to exit
	}
	relay.localProxies = make(map[uint64]*virtualGameProxy) // Overwrite the map, we don't need the old proxies anymore
}

func (relay *gameRelay) onBeginLinked() {
	relay.mutex.Lock() // Lock mutex since we're reading the list of local proxies
	defer relay.mutex.Unlock()

	if len(relay.localProxies) < 1 { // There shouldn't be any virtualgameproxys on link start, or host start either
		proxy := &virtualGameProxy{
			identifier:    0, // ID zero for when we are emulating a hosted game; when linked we only need one Game proxy
			socket:        nil,
			relay:         relay,
			signalChannel: make(chan channelSignal),
		}
		relay.localProxies[0] = proxy
		go proxy.relayDataLocalToRemote(common.DefaultVic2GamePort) // We want to open our socket on the Vic2 game port since we are emulating a hosted game

		relay.remoteSignalChannel <- BEGIN_LINKED_FORWARDING // Start allowing forwarding data from Relay server to the local game instance
	}
}

func (relay *gameRelay) onStopLinked() {
	relay.mutex.Lock() // Lock mutex since we're reading the list of local proxies
	defer relay.mutex.Unlock()

	relay.remoteSignalChannel <- STOP_FORWARDING

	proxy, found := relay.localProxies[0] // ID zero for when we are emulating a hosted game; when linked we only need one Game proxy
	if found {
		proxy.signalChannel <- SHUTDOWN
		<-proxy.signalChannel // Wait for thread to exit
		delete(relay.localProxies, 0)
	}
}
