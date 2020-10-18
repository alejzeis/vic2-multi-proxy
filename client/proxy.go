package client

import (
	"github.com/jython234/vic2-multi-proxy/common"
	log "github.com/sirupsen/logrus"
	"net"
	"sync"
	"time"
)

func processLocalData(client *proxyClient) {
	for {
		client.mutex.Lock()
		if !client.forwarding {
			time.Sleep(time.Second)
		}
		client.mutex.Unlock()

		buf := make([]byte, 2048)
		length, err := client.localConnection.Read(buf)
		if err != nil {
			log.WithField("addr", client.localConnection.RemoteAddr().String()).WithError(err).Warn("Error while reading buffer from local game instance connection.")

			client.mutex.Lock()
			client.localListener = nil
			client.mutex.Unlock()
			break
		}

		container := common.GameDataContainer{
			Relay:  false,
			Origin: uint16(client.localConnection.RemoteAddr().(*net.TCPAddr).Port),
			Data:   buf[0 : length-1],
		}
		_, err = client.remoteConnection.Write(container.Encode())
		if err != nil {
			log.WithError(err).Warn("Failed to send Local Game Data to Proxy Server")
		}
	}
}

type proxyClient struct {
	mutex *sync.Mutex

	serverAddress    string
	remoteConnection *net.TCPConn

	connected bool

	localListener   *net.TCPListener
	localConnection *net.TCPConn

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
	loopbackAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		panic(err)
	}

	listener, err2 := net.ListenTCP("tcp", loopbackAddr)
	if err2 != nil {
		log.WithError(err2).WithField("addr", addr).Error("Failed to start listening locally")
		panic(err2)
	}
	client.localListener = listener
	log.WithField("addr", addr).Info("Started listening locally")

	go client.listenIncoming()
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
	if client.forwarding && client.localConnection != nil {
		err := client.localConnection.Close()
		if err != nil {
			log.WithError(err).Error("Failed to close local connection while shutting down proxy client.")
		}
	}

	client.running = false
	client.forwarding = false
	err := client.localListener.Close()
	if err != nil {
		log.WithError(err).Error("Failed to close local listener while shutting down proxy client.")
	}
}

func (client *proxyClient) listenIncoming() {
	for {
		client.mutex.Lock()
		if !client.running {
			break
		}
		client.mutex.Unlock()

		conn, err := client.localListener.AcceptTCP()
		if err != nil {
			log.WithError(err).Error("Failed to accept TCP connection on local listener.")
			continue
		}

		if client.localConnection != nil {
			log.WithField("addr", conn.RemoteAddr().String()).Warn("Closed connection, there is already an active connection from a local game instance.")
			conn.Close()
			continue
		}

		err2 := conn.SetKeepAlive(true)
		if err2 != nil {
			log.WithError(err).Error("Failed to set keepalive for local game connection.")
		}
		client.localConnection = conn
		go processLocalData(client)
		log.WithField("addr", conn.RemoteAddr().String()).Debug("Accepted local game instance connection")
	}
}

func (client *proxyClient) setForwarding(forwarding bool) {
	client.mutex.Lock()
	defer client.mutex.Unlock()

	client.forwarding = forwarding
}
