package client

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	log "github.com/sirupsen/logrus"
)

// RunClient is the main method for running the client code
func RunClient() {
	log.Info("Client ready for commands.")
	scanner := bufio.NewScanner(os.Stdin)
	client := createRestClient("")
	proxy := createProxyClient()

	for {
		fmt.Print("> ")

		scanner.Scan()
		text := scanner.Text()

		if len(text) != 0 {
			if strings.HasPrefix(text, "connect") {
				if client.checkConnected() {
					log.Error("Already connected to a server, use \"disconnect\" command first")
				} else {
					// connect [server] [username]
					exploded := strings.Split(text, " ")
					if len(exploded) > 2 {
						log.WithField("url", exploded[1]).Info("Connecting to server...")
						client.serverURL = exploded[1]
						client.connect(exploded[2])
						proxy.onConnectMatchmaking(strings.ReplaceAll(exploded[2], "http://", ""))
					} else {
						log.Error("Usage: \"connect [URL] [username]\"")
					}
				}
			} else if strings.HasPrefix(text, "disconnect") {
				if !client.checkConnected() {
					log.Error("Not connected to a server, use \"connect\" command first")
				} else {
					client.disconnect()
					proxy.onDisconnectMatchmaking()
				}
			} else if strings.HasPrefix(text, "list") {
				processListCommand(client)
			} else if strings.HasPrefix(text, "link") {
				// link [name]
				exploded := strings.Split(text, " ")
				if len(exploded) > 1 {
					processLinkCommand(client, proxy, exploded[1])
				} else {
					log.Error("Usage: \"link [lobby Name]\"")
				}
			} else if strings.HasPrefix(text, "unlink") {
				if !client.checkConnected() || client.lastCheckin.LinkedLobby < 1 {
					log.Error("You must be connected to a server and linked to lobby first.")
				} else {
					client.unlink()
					proxy.setForwarding(false)
				}
			} else if strings.HasPrefix(text, "host") {
				if !client.checkConnected() {
					log.Error("You must be connected to a server first in order to host.")
				} else {
					exploded := strings.Split(text, " ")
					if len(exploded) > 1 {
						processHostCommand(client, scanner, exploded[1])
					} else {
						log.Error("Usage: \"host [start/stop]")
					}
				}
			} else {
				log.Warn("Unknown Command")
			}
		}
	}
}

func processListCommand(client *restClient) {
	client.mutex.Lock()
	defer client.mutex.Unlock()

	if !client.checkConnectedNoLock() {
		log.Error("Not connected to a server, use \"connect\" command first")
	} else {
		if len(client.lastCheckin.Lobbies) == 0 {
			log.Info("No lobbies found")
		} else {
			for key, val := range client.lastCheckin.Lobbies {
				log.WithFields(log.Fields{
					"name": key,
					"host": val.Host,
				}).Info("Lobby found")
			}
		}
	}
}

func processLinkCommand(client *restClient, proxy *proxyClient, lobbyName string) {
	client.mutex.Lock()

	if !client.checkConnectedNoLock() {
		log.Error("Not connected to a server, use \"connect\" command first")
	} else {
		foundLobby := ""

		for id, lobby := range client.lastCheckin.Lobbies {
			if strings.ToLower(lobbyName) == lobby.Name {
				foundLobby = id
			}
		}

		if foundLobby == "" {
			log.Error("That Lobby wasn't found. Please check spelling, or run \"list\" to see all lobbies")
		} else {
			log.WithFields(log.Fields{
				"host": client.lastCheckin.Lobbies[foundLobby].Host,
				"name": client.lastCheckin.Lobbies[foundLobby].Name,
			}).Info("OK, linking to lobby.")

		}

		client.mutex.Unlock()
		client.link(foundLobby)

		proxy.setForwarding(true)
	}
}

func processHostCommand(client *restClient, scanner *bufio.Scanner, operation string) {
	client.mutex.Lock()

	switch operation {
	case "start":
		if client.lastCheckin.Hosting {
			log.Error("You are already hosting a lobby!")
			break
		}

		fmt.Print("Enter the name for your lobby: ")
		scanner.Scan()
		lobbyName := scanner.Text()

		fmt.Print("\nEnter a password for the lobby (or leave blank): ")
		scanner.Scan()
		password := scanner.Text()

		client.mutex.Unlock()
		if client.host(lobbyName, password) {
			// TODO: Start relaying data
		}
		break
	case "stop":
		if !client.lastCheckin.Hosting {
			log.Error("You aren't hosting a lobby.")
			break
		}

		client.mutex.Unlock()
		if client.stopHost() {
			// TODO: Stop relaying data
		}
		break
	default:
		log.Error("Usage: host [start/stop]")
		client.mutex.Unlock()
		break
	}
}
