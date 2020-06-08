package main

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os"

	"github.com/jython234/vic2-multi-proxy/client"
	"github.com/jython234/vic2-multi-proxy/common"
	"github.com/jython234/vic2-multi-proxy/server"

	log "github.com/sirupsen/logrus"
	"gopkg.in/ini.v1"
)

func main() {
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
	})

	if len(os.Args) > 1 && os.Args[1] == "-server" {
		log.WithFields(log.Fields{
			"software": common.SoftwareName,
			"version":  common.SoftwareVersion,
			"mode":     "server",
		}).Info("Starting...")

		config := loadConfig()
		matchmaker := new(server.Matchmaker)

		server.StartControlServer(config, matchmaker)
	} else {
		log.WithFields(log.Fields{
			"software": common.SoftwareName,
			"version":  common.SoftwareVersion,
			"mode":     "client",
		}).Info("Starting...")

		client.RunClient()
	}
}

func loadConfig() *ini.File {
	var configLocation string = "server.ini"
	if os.Getenv("SERVER_CONFIG") != "" {
		configLocation = os.Getenv("SERVER_CONFIG")
	}

	file, err := ini.Load(configLocation)
	if err != nil {
		log.WithField("config", configLocation).WithError(err).Error("Failed to load configuration file.")
		panic(err)
	}

	return file
}

var hostConnection net.PacketConn

func runServer() {
	fmt.Println("Starting multiplayer proxy server on port 16322")
	serv, err := net.ListenPacket("udp", ":16322")

	if err != nil {
		panic(err)
	}

	defer serv.Close()

	for {
		tmp := make([]byte, 512)
		length, addr, _ := serv.ReadFrom(tmp)
		go serverHandlePacketFromClient(serv, tmp[:length], addr)
	}
}

func serverHandlePacketFromClient(connection net.PacketConn, data []byte, address net.Addr) {

}

var serverConnection net.PacketConn
var loopback net.PacketConn
var gameAddr net.Addr
var virtualSockets map[byte]net.Conn

func runClient(host bool) {
	fmt.Println("Starting Loopback server on port 1638")
	loopback, err := net.ListenPacket("udp", ":1638")

	if err != nil {
		panic(err)
	}

	go connectToServer(host)

	defer loopback.Close()

	for {
		tmp := make([]byte, 2048)
		length, gameAddr, _ := loopback.ReadFrom(tmp)
		go clientHandlePacketFromGame(loopback, tmp[:length], gameAddr)
	}
}

func connectToServer(host bool) {
	fmt.Println("Connecting to proxy server")

	tmp := make([]byte, 2048)
	serverConnection, err := net.Dial("udp", "wg.ajann.xyz:16322")
	if err != nil {
		panic(err)
	}

	defer serverConnection.Close()

	for {
		var bytesRead, err2 = bufio.NewReader(serverConnection).Read(tmp)
		if err2 == nil {
			if !host {
				loopback.WriteTo(tmp[1:bytesRead], gameAddr)
			} else {
				var virtualSocketId = tmp[0]
				if virtualSockets[virtualSocketId] == nil {
					fmt.Println("WARN: Creating virtual socket with ID")
					var newConn, err3 = net.Dial("udp", gameAddr.String())
					if err3 != nil {
						panic(err)
					} else {
						virtualSockets[virtualSocketId] = newConn
						virtualSockets[virtualSocketId].Write(tmp[1:bytesRead])
					}
				} else {
					virtualSockets[virtualSocketId].Write(tmp[1:bytesRead])
				}
			}
		} else {
			fmt.Printf("Some error %v\n", err)
		}
	}
}

func clientHandlePacketFromGame(connection net.PacketConn, data []byte, address net.Addr) {
	var final = make([]byte, 2048)
	var buffer = bytes.NewBuffer(final)
	buffer.WriteByte(0xAD)
	buffer.Write(data)
	serverConnection.WriteTo(buffer.Bytes()[:(len(data)+1)], &net.IPAddr{net.ParseIP("wg.ajann.xyz:16322"), ""})
}
