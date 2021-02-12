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
	relay := createStartRelay()

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
						if client.connect(exploded[2]) {
							if !relay.onConnectedToServer(exploded[1], client.authToken) {
								// Failed to connect on websocket, so logout from REST api
								client.disconnect()
							}
						}
					} else {
						log.Error("Usage: \"connect [URL] [username]\"")
					}
				}
			} else if strings.HasPrefix(text, "disconnect") {
				if !client.checkConnected() {
					log.Error("Not connected to a server, use \"connect\" command first")
				} else {
					client.disconnect()
					relay.onDisconnectedFromServer()
				}
			} else if strings.HasPrefix(text, "list") {
				processListCommand(client)
			} else if strings.HasPrefix(text, "link") {
				// link [name]
				exploded := strings.Split(text, " ")
				if !client.checkConnected() || client.lastCheckin.LinkedLobby > 0 {
					log.Error("You must be connected to a server and not already linked to a lobby first.")
				} else {
					processLinkCommand(client, relay, exploded[1])
				}
			} else if strings.HasPrefix(text, "unlink") {
				if !client.checkConnected() || client.lastCheckin.LinkedLobby < 1 {
					log.Error("You must be connected to a server and linked to a lobby first.")
				} else {
					client.unlink()
					relay.onStopLinked() // Tell relay to stop emulating the hosted game on localhost
				}
			} else if strings.HasPrefix(text, "host") {
				if !client.checkConnected() || client.lastCheckin.LinkedLobby > 0 {
					log.Error("You must be connected to a server first and not already linked to a lobby in order to host.")
				} else {
					exploded := strings.Split(text, " ")
					if len(exploded) > 1 {
						processHostCommand(client, relay, scanner, exploded[1])
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

func processLinkCommand(client *restClient, relay *gameRelay, lobbyName string) {
	client.mutex.Lock()

	foundLobby := ""

	for id, lobby := range client.lastCheckin.Lobbies {
		if strings.ToLower(lobbyName) == lobby.Name {
			foundLobby = id
		}
	}

	if foundLobby == "" {
		log.Error("That Lobby wasn't found. Please check spelling, or run \"list\" to see all lobbies")
		return
	} else {
		log.WithFields(log.Fields{
			"host": client.lastCheckin.Lobbies[foundLobby].Host,
			"name": client.lastCheckin.Lobbies[foundLobby].Name,
		}).Info("OK, linking to lobby.")

	}

	client.mutex.Unlock()
	client.link(foundLobby)
	relay.onBeginLinked() // Tell relay to start emulating the hosted game on localhost
}

func processHostCommand(client *restClient, relay *gameRelay, scanner *bufio.Scanner, operation string) {
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
			relay.onBeginHosting() // Tell relay to start emulating game clients to the local hosted game
		}
		break
	case "stop":
		if !client.lastCheckin.Hosting {
			log.Error("You aren't hosting a lobby.")
			break
		}

		client.mutex.Unlock()
		if client.stopHost() {
			relay.onStopHosting() // Tell relay to stop emulating game clients to the local hosted game
		}
		break
	default:
		log.Error("Usage: host [start/stop]")
		client.mutex.Unlock()
		break
	}
}
