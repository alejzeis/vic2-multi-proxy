package client

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/alejzeis/vic2-multi-proxy/common"

	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

type restClient struct {
	rest      *resty.Client
	serverURL string

	serverInfo  common.InfoResponse
	lastCheckin common.CheckinResponse

	authToken     string
	lastRenewedAt time.Time
	mutex         *sync.Mutex

	checkinChannel chan bool

	relay GameDataRelay
}

func newRestClient(serverURL string, relay GameDataRelay) *restClient {
	client := new(restClient)
	client.checkinChannel = make(chan bool, 1) // Buffer channel so we have non-blocking send
	client.checkinChannel <- false             // Signal checkin goroutine to not attempt doing checkins
	go client.continuousCheckin()              // Start the checkin goroutine

	client.serverURL = serverURL
	client.rest = resty.New()
	client.mutex = new(sync.Mutex)
	client.relay = relay
	return client
}

// goroutine that retrieves the latest checkin and renews token when applicable
func (rc *restClient) continuousCheckin() {
	ticker := time.NewTicker(1 * time.Second)
	sleeping := false
	for range ticker.C {
		select {
		case doCheckin, notClosed := <-rc.checkinChannel:
			if !notClosed {
				return // We've been told to stop
			} else {
				sleeping = !doCheckin
			}
		default:
			// Non blocking receive
		}

		if !sleeping {
			rc.mutex.Lock()

			success := rc.checkin()
			if success {
				if rc.lastRenewedAt.Add(30 * time.Second).Before(time.Now()) {
					// It's been 30 seconds, time to renew the token
					rc.renewToken()
				}
			} else {
				// Something failed, most likely either the server died or our token expired
				// Set our state to disconnected then:
				rc.completeDisconnect()
			}

			rc.mutex.Unlock()
		}
	}

	log.Debug("Checkin exited")
}

func (rc *restClient) GetStatus() (bool, common.CheckinResponse) {
	rc.mutex.Lock()
	defer rc.mutex.Unlock()

	if rc.authToken == "" {
		return false, common.CheckinResponse{}
	}

	expireTime := rc.lastRenewedAt.Add(2 * time.Minute)
	if time.Now().After(expireTime) {
		// Token was expired, we're not connected anymore
		rc.checkinChannel <- false // Stop checkins
		rc.authToken = ""
		return false, common.CheckinResponse{}
	}

	return true, rc.lastCheckin
}

func (rc *restClient) checkConnectedNoLock() bool {
	if rc.authToken == "" {
		return false
	}

	expireTime := rc.lastRenewedAt.Add(2 * time.Minute)
	if time.Now().After(expireTime) {
		// Token was expired, we're not connected anymore
		rc.checkinChannel <- false // Stop checkins
		rc.authToken = ""
		return false
	}

	return true
}

// not thread safe, lock the mutex before calling this
func (rc *restClient) renewToken() {
	url := rc.serverURL + "/renew/" + rc.authToken
	response, err := rc.rest.R().Get(url)
	if err != nil {
		log.WithField("url", url).WithError(err).Warn("Failed to renew token.")
	} else if response.StatusCode() != http.StatusOK {
		log.WithFields(log.Fields{
			"url":    url,
			"status": response.StatusCode(),
			"body":   response.Body(),
		}).Warn("Failed to renew token")
	} else {
		log.Debug("Renewed token")
		rc.authToken = response.String()
		rc.lastRenewedAt = time.Now()
	}
}

// not thread safe, lock the mutex before calling this
// Attempts to call /checkin on the REST server. Returns if the checkin was successful or not
func (rc *restClient) checkin() bool {
	url := rc.serverURL + "/checkin/" + rc.authToken
	response, err := rc.rest.R().Get(url)
	if err != nil {
		log.WithField("url", url).WithError(err).Error("Error while processing checkin (server not responding?)")
		return false
	} else if response.StatusCode() != http.StatusOK {
		log.WithFields(log.Fields{
			"url":    url,
			"status": response.StatusCode(),
			"body":   response.Body(),
		}).Warn("Unexpected response while processing checkin (did our token expire?)")
		return false
	} else {
		linkedToLobbyPrior := rc.lastCheckin.LinkedLobby > 0

		decodeErr := json.Unmarshal(response.Body(), &rc.lastCheckin)
		if decodeErr != nil {
			log.WithFields(log.Fields{
				"url":  url,
				"body": response.String(),
			}).WithError(err).Error("Failed to decode JSON response while processing checkin.")
		}

		if linkedToLobbyPrior && rc.lastCheckin.LinkedLobby == 0 {
			log.Error("Remote Lobby closed.")
			rc.relay.OnStopLinked()
		}

		return true
	}
}

func (rc *restClient) Connect(username string) bool {
	rc.mutex.Lock()
	defer rc.mutex.Unlock()

	if rc.checkConnectedNoLock() {
		return false
	}

	url := rc.serverURL + "/login/" + username
	response, err := rc.rest.R().Post(url)
	if err != nil {
		log.WithFields(log.Fields{
			"url":    url,
			"status": response.StatusCode(),
			"body":   response.Body(),
		}).WithError(err).Error("Failed to login")
		return false
	} else if response.StatusCode() != http.StatusCreated {
		log.WithFields(log.Fields{
			"url":    url,
			"status": response.StatusCode(),
			"body":   response.Body(),
		}).Error("Failed to login")
		return false
	} else {
		log.Info("Successfully logged into server.")

		// Attempt relay connection
		if rc.relay.OnConnectedToServer(rc.serverURL, rc.authToken) {
			rc.checkinChannel <- true // Signal checkin goroutine to do checkins periodically
			rc.authToken = response.String()
			rc.lastRenewedAt = time.Now()
		} else {
			log.Error("Relay failed to connect to server, logging out.")
			rc.Logout()
		}

		return true
	}
}

func (rc *restClient) Logout() {
	rc.mutex.Lock()
	defer rc.mutex.Unlock()

	if !rc.checkConnectedNoLock() {
		return
	}

	url := rc.serverURL + "/logout/" + rc.authToken
	response, err := rc.rest.R().Get(url)
	if err != nil {
		log.WithField("url", url).WithError(err).Error("Failed to logout")
	} else if response.StatusCode() != http.StatusNoContent {
		log.WithFields(log.Fields{
			"url":    url,
			"status": response.StatusCode(),
			"body":   response.Body(),
		}).Error("Failed to logout")
	} else {
		log.Info("Successfully logged out of server.")
		rc.completeDisconnect()
	}
}

// Not thread safe
// TODO: rename to disconnect()
func (rc *restClient) completeDisconnect() {
	rc.checkinChannel <- false // Stop periodic checkins
	rc.authToken = ""

	rc.relay.OnDisconnectedFromServer()
}

func (rc *restClient) Link(lobbyIdStr string) bool {
	rc.mutex.Lock()
	defer rc.mutex.Unlock()

	if !rc.checkConnectedNoLock() {
		return false
	}

	url := rc.serverURL + "/link/" + rc.authToken + "/" + lobbyIdStr
	response, err := rc.rest.R().Put(url)
	if err != nil || response.StatusCode() != http.StatusNoContent {
		log.WithFields(log.Fields{
			"url":     url,
			"status":  response.StatusCode(),
			"body":    response.Body(),
			"lobbyId": lobbyIdStr,
		}).WithError(err).Error("Failed to link to lobby")
		return false
	} else {
		log.WithFields(log.Fields{
			"id":   lobbyIdStr,
			"name": rc.lastCheckin.Lobbies[lobbyIdStr].Name,
		}).Info("Successfully linked to lobby ")

		lobbyId, _ := strconv.Atoi(lobbyIdStr)
		rc.lastCheckin.LinkedLobby = uint64(lobbyId)

		rc.relay.OnBeginLinked()

		return true
	}
}

func (rc *restClient) Unlink() bool {
	rc.mutex.Lock()
	defer rc.mutex.Unlock()

	if !rc.checkConnectedNoLock() {
		return false
	}

	url := rc.serverURL + "/link/" + rc.authToken + "/0"
	response, err := rc.rest.R().Put(url)
	if err != nil || response.StatusCode() != http.StatusNoContent {
		log.WithFields(log.Fields{
			"url":    url,
			"status": response.StatusCode(),
			"body":   response.Body(),
		}).WithError(err).Error("Failed to unlink")
		return false
	} else {
		log.Info("Successfully unlinked from lobby")

		rc.lastCheckin.LinkedLobby = 0
		rc.relay.OnStopLinked()

		return true
	}
}

func (rc *restClient) Host(lobbyName string, password string) bool {
	rc.mutex.Lock()
	defer rc.mutex.Unlock()

	if !rc.checkConnectedNoLock() {
		return false
	}

	url := rc.serverURL + "/host/" + rc.authToken
	response, err := rc.rest.R().SetFormData(map[string]string{
		"name":     lobbyName,
		"password": password,
	}).Put(url)
	if err != nil || response.StatusCode() != http.StatusNoContent {
		log.WithFields(log.Fields{
			"url":    url,
			"status": response.StatusCode(),
			"body":   response.Body(),
		}).WithError(err).Error("Failed to create lobby")
		return false
	} else {
		log.Info("Successfully created lobby and now hosting")

		rc.lastCheckin.Hosting = true
		rc.relay.OnBeginHosting()

		return true
	}
}

func (rc *restClient) StopHost() bool {
	rc.mutex.Lock()
	defer rc.mutex.Unlock()

	if !rc.checkConnectedNoLock() {
		return false
	}

	url := rc.serverURL + "/host/" + rc.authToken
	response, err := rc.rest.R().Delete(url)
	if err != nil || response.StatusCode() != http.StatusNoContent {
		log.WithFields(log.Fields{
			"url":    url,
			"status": response.StatusCode(),
			"body":   response.Body(),
		}).WithError(err).Error("Failed to delete lobby")
		return false
	} else {
		log.Info("Successfully deleted lobby and stopped hosting")

		rc.lastCheckin.Hosting = false
		rc.relay.OnStopHosting()

		return true
	}
}
