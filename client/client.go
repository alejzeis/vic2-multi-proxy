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

	for {
		fmt.Print("> ")

		scanner.Scan()
		text := scanner.Text()
		if len(text) != 0 {
			if strings.HasPrefix(text, "connect") {
				// connect [server]
				exploded := strings.Split(text, " ")
				if len(exploded) > 1 {
					// TODO connect sequence
				} else {
					log.Error("Usage: \"connect [URL]\"")
				}
			}
		}
	}
}

func connectedMainLoop() {

}
