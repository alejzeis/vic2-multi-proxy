package client

import (
	"bufio"
	"fmt"
	"github.com/alejzeis/vic2-multi-proxy/common"
	"os"
	"strings"

	log "github.com/sirupsen/logrus"
)

// Represents a source for commands for the client to process
// This is implemented with ConsoleCommandSource, but can be something else to source commands from,
// such as in a GUI application
type CommandSource interface {
	// Called by the client to get the next command for it to process.
	GetNextCommand() string
}

// Simple Implementation of CommandSource, which just reads commands from stdin
type ConsoleCommandSource struct {
	scanner *bufio.Scanner
}

func NewConsoleCommandSource() *ConsoleCommandSource {
	ccs := new(ConsoleCommandSource)
	ccs.scanner = bufio.NewScanner(os.Stdin)
	return ccs
}

func (ccs *ConsoleCommandSource) GetNextCommand() string {
	fmt.Print("> ")
	ccs.scanner.Scan()
	return ccs.scanner.Text()
}

type Client struct {
	commandSource CommandSource
	rClient       *restClient

	running bool
}

func NewClient(commandSource CommandSource, relay GameDataRelay) *Client {
	client := new(Client)
	client.commandSource = commandSource
	client.rClient = newRestClient("", relay)
	client.running = true
	return client
}

// Main loop for processing commands
func (client *Client) RunClient() {
	log.Info("Client ready for commands.")

	for client.running {
		cmd := client.commandSource.GetNextCommand()
		if len(cmd) != 0 {
			client.processCommand(cmd)
		}
	}
}

func (client *Client) processCommand(text string) {
	connected, checkinSnapshot := client.rClient.GetStatus()

	if strings.HasPrefix(text, "connect") {
		if connected {
			log.Error("Already connected to a server, use \"disconnect\" command first")
		} else {
			// connect [server] [username]
			exploded := strings.Split(text, " ")
			if len(exploded) > 2 {
				log.WithField("url", exploded[1]).Info("Connecting to server...")
				client.rClient.serverURL = exploded[1]
				client.rClient.Connect(exploded[2])
			} else {
				log.Error("Usage: \"connect [URL] [username]\"")
			}
		}
	} else if strings.HasPrefix(text, "disconnect") {
		if connected {
			client.rClient.Logout()
		} else {
			log.Error("Not connected to a server, use \"connect\" command first")
		}
	} else if strings.HasPrefix(text, "list") {
		client.processListCommand(connected, checkinSnapshot)
	} else if strings.HasPrefix(text, "link") {
		// link [name]
		exploded := strings.Split(text, " ")
		if len(exploded) > 1 {
			client.processLinkCommand(connected, checkinSnapshot, exploded[1])
		} else {
			log.Error("Usage: \"link [name]\"")
		}
	} else if strings.HasPrefix(text, "unlink") {
		if !connected || checkinSnapshot.LinkedLobby < 1 {
			log.Error("You must be connected to a server and linked to a lobby first.")
		} else {
			client.rClient.Unlink()
		}
	} else if strings.HasPrefix(text, "host") {
		exploded := strings.Split(text, " ")
		if len(exploded) == 2 {
			client.processHostCommand(connected, checkinSnapshot, exploded[1], "")
		} else if len(exploded) > 2 {
			client.processHostCommand(connected, checkinSnapshot, exploded[1], exploded[2])
		} else {
			log.Error("Usage: \"host start [lobbyName]\" OR \"host stop\"")
		}
	} else {
		log.Warn("Unknown Command")
	}
}

func (client *Client) processListCommand(connected bool, checkinSnapshot common.CheckinResponse) {
	if connected {
		if len(checkinSnapshot.Lobbies) == 0 {
			log.Info("No lobbies found")
		} else {
			for key, val := range checkinSnapshot.Lobbies {
				log.WithFields(log.Fields{
					"name": key,
					"host": val.Host,
				}).Info("Lobby found")
			}
		}
	} else {
		log.Error("Not connected to a server, use \"connect\" command first")
	}
}

func (client *Client) processLinkCommand(connected bool, checkinSnapshot common.CheckinResponse, lobbyName string) {
	if connected && !checkinSnapshot.Hosting && checkinSnapshot.LinkedLobby < 1 {
		foundLobby := ""

		for id, lobby := range checkinSnapshot.Lobbies {
			if strings.ToLower(lobbyName) == lobby.Name {
				foundLobby = id
			}
		}

		if foundLobby == "" {
			log.Error("That Lobby wasn't found. Please check spelling, or run \"list\" to see all lobbies")
		} else {
			log.WithFields(log.Fields{
				"host": checkinSnapshot.Lobbies[foundLobby].Host,
				"name": checkinSnapshot.Lobbies[foundLobby].Name,
			}).Info("OK, linking to lobby.")

			client.rClient.Link(foundLobby)
		}
	} else if connected && checkinSnapshot.Hosting {
		log.Error("You are hosting a lobby, you must not be hosting to link")
	} else if connected && checkinSnapshot.LinkedLobby > 0 {
		log.Error("You are already linked to a lobby, unlink first")
	} else {
		log.Error("Not connected to a server, use \"connect\" command first")
	}
}

func (client *Client) processHostCommand(connected bool, checkinSnapshot common.CheckinResponse, operation string, lobbyName string) {
	if connected && checkinSnapshot.LinkedLobby < 1 {
		switch operation {
		case "start":
			if checkinSnapshot.Hosting {
				log.Error("You are already hosting a lobby!")
			} else {
				client.rClient.Host(lobbyName, "")
			}
			break
		case "stop":
			if checkinSnapshot.Hosting {
				client.rClient.StopHost()
			} else {
				log.Error("You aren't hosting a lobby.")
			}
			break
		default:
			log.Error("Usage: \"host start [lobbyName]\" OR \"host stop\"")
			break
		}
	} else if connected {
		log.Error("You are linked to a lobby! Unlink first, then try to host")
	} else {
		log.Error("Not connected to a server, use \"connect\" command first")
	}
}
