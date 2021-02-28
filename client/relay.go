package client

import (
	"errors"
	"github.com/alejzeis/vic2-multi-proxy/common"
	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
	"net"
	"strconv"
	"strings"
	"sync"
)

func CreateStartRelay() *gameDataRelayImpl {
	relay := new(gameDataRelayImpl)
	relay.mutex = new(sync.Mutex)
	relay.localProxies = make(map[uint64]*virtualGameProxy)
	relay.remoteSignalChannel = make(chan channelSignal, 1)
	relay.connectionsWaitgroup = new(sync.WaitGroup)
	relay.hasClosed = false

	return relay
}

type channelSignal uint8

const (
	DISCONNECTED_MATCHMAKING channelSignal = iota
	BEGIN_HOSTING_FORWARDING
	BEGIN_LINKED_FORWARDING
	STOP_FORWARDING
)

type GameDataRelay interface {
	OnConnectedToServer(address string, auth string) bool
	OnDisconnectedFromServer()
	OnBeginHosting()
	OnStopHosting()
	OnBeginLinked()
	OnStopLinked()
}

type gameDataRelayImpl struct {
	mutex            *sync.Mutex
	serverAddress    string
	remoteConnection *websocket.Conn

	localProxies map[uint64]*virtualGameProxy

	connectionsWaitgroup *sync.WaitGroup
	hasClosed            bool

	remoteSignalChannel chan channelSignal
}

// Represents a "proxied" game connected to the local actual game
// This is either one of various clients (if the local game is hosting)
// Or a virtual "server" (if the local game is linked to a remote lobby)
type virtualGameProxy struct {
	// User ID that this proxied game belongs to
	identifier  uint64
	socket      *net.UDPConn
	gameAddress *net.UDPAddr

	relay GameDataRelay

	signalChannel chan channelSignal
}

func (proxy *virtualGameProxy) initSocket(port uint16) {
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
	}).Debug("Created socket for relaying local game data")
}

// When hosting, each fake "client" will have their own UDP socket and thread instance of this
func (proxy *virtualGameProxy) relayDataLocalToRemote(hosting bool, waitgroup *sync.WaitGroup) {
	defer func() {
		defer waitgroup.Done()
		err := proxy.socket.Close()
		if err != nil {
			log.WithError(err).WithFields(log.Fields{
				"identifier":   proxy.identifier,
				"localAddress": proxy.socket.LocalAddr().String(),
			}).Error("Failed to close local UDP socket for virtualGameProxy.")
		}

		log.WithField("proxyIdentifier", proxy.identifier).Debug("Stopped listening for local game data")
	}()

	if hosting {
		gameAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:1630")
		if err != nil {
			log.WithError(err).WithField("address", "127.0.0.1:1630").Error("Failed to resolve game address")
			return
		} else {
			proxy.gameAddress = gameAddr
		}
	}

	waitgroup.Add(1) // Add ourselves to the waitgroup. This allows the shutdown() function to wait for all goroutines to exit

	for {
		buf := make([]byte, 2048)
		length, address, err := proxy.socket.ReadFromUDP(buf)
		if err != nil {
			log.WithError(err).WithFields(log.Fields{
				"localAddress":    proxy.socket.LocalAddr().String(),
				"remoteAddress":   address,
				"proxyIdentifier": proxy.identifier,
			}).Error("Error while reading data from local listening socket")
		} else if proxy.gameAddress == nil {
			proxy.gameAddress = address
		}

		select {
		case _, notClosed := <-proxy.signalChannel:
			if !notClosed {
				return // Exit function, stopping the thread
			}
		default:
			// We want to do a non-blocking receive
		}

		container := common.GameDataContainer{
			Identifier: proxy.identifier,
			Data:       buf[0:(length - 1)],
		}

		proxy.relay.sendContainerToServer(container)
	}
}

func (proxy *virtualGameProxy) sendGamePacket(container common.GameDataContainer) error {
	if proxy.gameAddress == nil {
		return errors.New("game Address hasn't been obtained yet")
	} else {
		_, err := proxy.socket.WriteToUDP(container.Encode(), proxy.gameAddress)
		if err != nil {
			return err
		} else {
			return nil
		}
	}
}

func (relay *gameDataRelayImpl) sendContainerToServer(container common.GameDataContainer) {
	err := relay.remoteConnection.WriteMessage(websocket.BinaryMessage, container.Encode())
	if err != nil {
		log.WithFields(log.Fields{
			"relayAddr":       relay.serverAddress,
			"proxyIdentifier": container.Identifier,
			"length":          len(container.Data),
		}).WithError(err).Warn("Failed to relay a packet from local game to relay server")
	}
}

func (relay *gameDataRelayImpl) shutdown() {
	for _, proxy := range relay.localProxies {
		close(proxy.signalChannel)
		proxy.socket.Close() // Close the socket, since the UDP reads are blocking we need to close the socket to get the goroutines to terminate
	}
	close(relay.remoteSignalChannel)

	log.WithField("proxyCount", len(relay.localProxies)).Debug("Sent shutdown signal to all proxy goroutines, waiting for exits")

	relay.connectionsWaitgroup.Wait()

	log.WithField("proxyCount", len(relay.localProxies)).Debug("All proxy goroutines have exited")

	relay.serverAddress = ""
	relay.localProxies = make(map[uint64]*virtualGameProxy) // Overwrite the map, we don't need the old proxies anymore
}

func (relay *gameDataRelayImpl) relayDataRemoteToLocal() {
	forwarding := false
	hosting := false
	defer func() {
		err := relay.remoteConnection.Close()
		if err != nil {
			log.WithError(err).WithField("address", relay.serverAddress).Error("Failed to close websocket connection to relay server.")
		}

		relay.remoteConnection = nil
		log.Debug("Remote to Local relay goroutine exited.")
	}()

	for {
		select {
		case signal, notClosed := <-relay.remoteSignalChannel:
			if notClosed {
				switch signal {
				case DISCONNECTED_MATCHMAKING:
					return // Exit function, stopping the thread
				case STOP_FORWARDING:
					forwarding = false
				case BEGIN_HOSTING_FORWARDING:
					forwarding = true
					hosting = true
				case BEGIN_LINKED_FORWARDING:
					forwarding = true
				}
			} else { // Channel closed, that's the signal to terminate
				return // Exit function, stopping the thread
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
			} else {
				log.WithError(err).Error("Failed to read message from remote websocket connection")
			}
			relay.onRemoteDisconnected()
		} else if msgType == websocket.BinaryMessage && forwarding {
			container := common.DecodeGameDataContainer(data)

			relay.mutex.Lock()

			proxy, found := relay.localProxies[container.Identifier]
			if !found && hosting {
				// Proxy instance wasn't found, so it must be a new connection.
				// Create a new virtualGameProxy to represent this connection
				proxy = &virtualGameProxy{
					identifier:    container.Identifier,
					socket:        nil,
					relay:         relay,
					signalChannel: make(chan channelSignal, 1),
				}
				relay.localProxies[proxy.identifier] = proxy
				proxy.initSocket(0) // Don't have a specific port to bind to since we are emulating a client
				go proxy.relayDataLocalToRemote(true, relay.connectionsWaitgroup)

				log.WithField("identifier", proxy.identifier).Debug("Created new virtualgame proxy")
			}

			if hosting {
				err = proxy.sendGamePacket(container)
				if err != nil {
					log.WithError(err).WithFields(log.Fields{
						"identifier":   container.Identifier,
						"length":       len(container.Data),
						"gameAddress":  proxy.gameAddress,
						"relayAddress": relay.serverAddress,
						"hosting":      hosting,
					}).Warn("Failed to send packet from remote to local game")
				}
			}

			relay.mutex.Unlock()
		}
	}
}

func (relay *gameDataRelayImpl) OnConnectedToServer(address string, auth string) bool {
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
		return false
	}

	connection, _, err := websocket.DefaultDialer.Dial(relay.serverAddress+"/relay", nil)
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
	relay.hasClosed = false
	go relay.relayDataRemoteToLocal()
	return true
}

// Called from relayDataRemoteToLocal when the server closes the connection on us
func (relay *gameDataRelayImpl) onRemoteDisconnected() {
	relay.mutex.Lock()
	defer relay.mutex.Unlock()

	relay.shutdown()
}

func (relay *gameDataRelayImpl) OnDisconnectedFromServer() {
	relay.mutex.Lock()
	defer relay.mutex.Unlock()

	// Make sure we haven't already completed disconnect tasks
	if relay.remoteConnection != nil {
		err := relay.remoteConnection.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "Logging out"))
		if err != nil {
			panic(err)
		}
		relay.hasClosed = true
	}
}

func (relay *gameDataRelayImpl) OnBeginHosting() {
	relay.mutex.Lock() // Lock mutex since we're reading the list of local proxies
	defer relay.mutex.Unlock()

	if len(relay.localProxies) < 1 { // There shouldn't be any virtualgameproxys on host start, or link start either
		// No need to create any virtualgameproxy instances,
		// the relayDataRemoteToLocal function will automatically create them as packets come in from Relay server
		relay.remoteSignalChannel <- BEGIN_HOSTING_FORWARDING // Start allowing forwarding data from Relay server to the local game instance
	}
}

func (relay *gameDataRelayImpl) OnStopHosting() {
	relay.mutex.Lock() // Lock mutex since we're reading the list of local proxies
	defer relay.mutex.Unlock()

	relay.remoteSignalChannel <- STOP_FORWARDING

	for _, proxy := range relay.localProxies {
		close(proxy.signalChannel)
		proxy.socket.Close() // Close the socket, since the UDP reads are blocking we need to close the socket to get the goroutines to terminate
	}
	relay.localProxies = make(map[uint64]*virtualGameProxy) // Overwrite the map, we don't need the old proxies anymore
}

func (relay *gameDataRelayImpl) OnBeginLinked() {
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
		proxy.initSocket(common.DefaultVic2GamePort) // We want to open our socket on the Vic2 game port since we are emulating a hosted game
		go proxy.relayDataLocalToRemote(false, relay.connectionsWaitgroup)

		relay.remoteSignalChannel <- BEGIN_LINKED_FORWARDING // Start allowing forwarding data from Relay server to the local game instance
	}
}

func (relay *gameDataRelayImpl) OnStopLinked() {
	relay.mutex.Lock() // Lock mutex since we're reading the list of local proxies
	defer relay.mutex.Unlock()

	relay.remoteSignalChannel <- STOP_FORWARDING

	proxy, found := relay.localProxies[0] // ID zero for when we are emulating a hosted game; when linked we only need one Game proxy
	if found {
		close(proxy.signalChannel)
		proxy.socket.Close() // Close the socket, since the UDP reads are blocking we need to close the socket to get the goroutines to terminate
		delete(relay.localProxies, 0)
	}
}
