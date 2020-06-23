package client

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/jython234/vic2-multi-proxy/common"

	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

// goroutine that retrieves the latest checkin and renews token when applicable
func continousCheckin(client *restClient) {
	ticker := time.NewTicker(1 * time.Second)
	for range ticker.C {
		client.mutex.Lock()
		if client.stop {
			client.mutex.Unlock()
			break
		}

		client.checkin()

		if client.lastRenewedAt.Add(30 * time.Second).Before(time.Now()) {
			// It's been 30 seconds, time to renew the token
			client.renewToken()
		}

		client.mutex.Unlock()
	}
}

type restClient struct {
	rest      *resty.Client
	serverURL string

	serverInfo  common.InfoResponse
	lastCheckin common.CheckinResponse

	stop          bool
	authToken     string
	lastRenewedAt time.Time
	mutex         *sync.Mutex
}

func createRestClient(serverURL string) *restClient {
	client := new(restClient)
	client.stop = true
	client.serverURL = serverURL
	client.rest = resty.New()
	client.mutex = &sync.Mutex{}
	return client
}

func (r *restClient) checkConnected() bool {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if r.authToken == "" {
		return false
	}

	expireTime := r.lastRenewedAt.Add(2 * time.Minute)
	if time.Now().After(expireTime) {
		// Token was expired, we're not connected anymore
		r.authToken = ""
		return false
	}

	return true
}

// not thread safe, lock the mutex before calling this
func (r *restClient) renewToken() {
	url := r.serverURL + "/renew/" + r.authToken
	response, err := r.rest.R().Get(url)
	if err != nil {
		log.WithField("url", url).WithError(err).Warn("Failed to renew token.")
		return
	} else if response.StatusCode() != http.StatusOK {
		log.WithFields(log.Fields{
			"url":    url,
			"status": response.StatusCode(),
			"body":   response.Body(),
		}).Warn("Failed to renew token")
		return
	}

	log.Debug("Renewed token")
	r.authToken = response.String()
	r.lastRenewedAt = time.Now()
}

// not thread safe, lock the mutex before calling this
func (r *restClient) checkin() {
	url := r.serverURL + "/checkin/" + r.authToken
	response, err := r.rest.R().Get(url)
	if err != nil {
		log.WithField("url", url).WithError(err).Warn("Failed to process checkin.")
		return
	} else if response.StatusCode() != http.StatusOK {
		log.WithFields(log.Fields{
			"url":    url,
			"status": response.StatusCode(),
			"body":   response.Body(),
		}).Warn("Failed to process checkin")
		return
	}

	decodeErr := json.Unmarshal(response.Body(), &r.lastCheckin)
	if decodeErr != nil {
		log.WithFields(log.Fields{
			"url":  url,
			"body": response.String(),
		}).WithError(err).Error("Failed to decode JSON response while processing checkin.")
	}
}

func (r *restClient) connect(username string) bool {
	if r.checkConnected() {
		return false
	}

	r.mutex.Lock()
	defer r.mutex.Unlock()

	url := r.serverURL + "/login/" + username
	response, err := r.rest.R().Post(url)
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
	}

	log.Info("Successfully logged into server.")
	r.stop = false
	r.authToken = response.String()
	r.lastRenewedAt = time.Now()

	go continousCheckin(r)

	return true
}

func (r *restClient) disconnect() {
	if !r.checkConnected() {
		return
	}

	r.mutex.Lock()
	defer r.mutex.Unlock()

	url := r.serverURL + "/logout/" + r.authToken
	response, err := r.rest.R().Get(url)
	if err != nil {
		log.WithField("url", url).WithError(err).Error("Failed to logout")
		return
	} else if response.StatusCode() != http.StatusNoContent {
		log.WithFields(log.Fields{
			"url":    url,
			"status": response.StatusCode(),
			"body":   response.Body(),
		}).Error("Failed to logout")
		return
	}

	log.Info("Successfully logged out of server.")
	r.stop = true
	r.authToken = ""
}
