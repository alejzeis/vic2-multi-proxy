package client

import (
	"github.com/jython234/vic2-multi-proxy/common"
	log "github.com/sirupsen/logrus"
	"net"
	"sync"
)

func processLocalData(client *proxyClient) {
	log.Info("Started listening for local game data")
	for {
		buf := make([]byte, 2048)
		length, err := client.localListener.Read(buf)
		if err != nil {
			log.WithField("addr", client.localListener.RemoteAddr().String()).WithError(err).Error("Error while reading data from local listening socket")
			break
		}

		container := common.GameDataContainer{
			ToServer: true,
			Data:     buf[0 : length-1],
		}
		_, err = client.remoteConnection.Write(container.Encode())
		if err != nil {
			log.WithError(err).Warn("Failed to send Local Game Data to Proxy Server")
		}
	}
	log.Info("Stopped listening for local game data")
}

type proxyClient struct {
	mutex *sync.Mutex

	serverAddress    string
	remoteConnection *net.TCPConn

	connected bool

	localListener *net.UDPConn

	forwarding bool
	running    bool
}

func createProxyClient() *proxyClient {
	client := new(proxyClient)
	client.connected = false
	client.forwarding = false
	client.running = false
	client.mutex = &sync.Mutex{}
	return client
}

func (client *proxyClient) startup() {
	if client.running {
		return
	}

	client.running = true
	addr := "127.0.0.1:1630"
	loopbackAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		log.WithError(err).WithField("addr", addr).Error("Failed to resolve loopback address")
		panic(err)
	}

	listener, err2 := net.ListenUDP("tcp", loopbackAddr)
	if err2 != nil {
		log.WithError(err2).WithField("addr", addr).Error("Failed to start listening locally")
		panic(err2)
	}
	client.localListener = listener
	log.WithField("addr", addr).Info("Started listening locally")

	go processLocalData(client)
}

func (client *proxyClient) shutdown() {
	client.mutex.Lock()
	defer client.mutex.Unlock()

	if client.connected && client.remoteConnection != nil {
		err := client.remoteConnection.Close()
		if err != nil {
			log.WithError(err).Error("Failed to close remote connection while shutting down proxy client.")
		}
	}

	client.running = false
	client.forwarding = false
	err := client.localListener.Close()
	if err != nil {
		log.WithError(err).Error("Failed to close local listener while shutting down proxy client.")
	}
}

func (client *proxyClient) onConnectMatchmaking(address string) {
	client.serverAddress = address + ":16322"
	tcpAddr, err := net.ResolveTCPAddr("tcp", client.serverAddress)
	if err != nil {
		log.WithError(err).WithField("address", address).Error("Failed to resolve remote server address for proxying!")
		panic(err)
	}

	conn, err := net.DialTCP("tcp", nil, tcpAddr)
	if err != nil {
		log.WithError(err).WithField("address", address).Error("Failed to connect to remote server address for proxying!")
		panic(err)
	}

	client.remoteConnection = conn
	_ = client.remoteConnection.SetKeepAlive(true)
}

func (client *proxyClient) onDisconnectMatchmaking() {
	err := client.remoteConnection.Close()
	if err != nil {
		log.WithError(err).WithField("address", client.serverAddress).Error("Failed to close connected to remote server for proxying.")
	}
}

func (client *proxyClient) setForwarding(forwarding bool) {
	client.mutex.Lock()
	defer client.mutex.Unlock()

	client.forwarding = forwarding
}
