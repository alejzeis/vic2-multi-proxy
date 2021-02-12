package main

import (
	"os"

	"github.com/alejzeis/vic2-multi-proxy/client"
	"github.com/alejzeis/vic2-multi-proxy/common"
	"github.com/alejzeis/vic2-multi-proxy/server"

	log "github.com/sirupsen/logrus"
	"gopkg.in/ini.v1"
)

func main() {
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
	})
	log.SetLevel(log.DebugLevel)

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
