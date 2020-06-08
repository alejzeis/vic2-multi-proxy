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

// keepalive goroutine that keeps renewing the auth token
func keepalive(client *restClient) {
	ticker := time.NewTicker(30 * time.Second)
	for range ticker.C {
		client.mutex.Lock()
		if client.stop {
			break
		}

		client.renewToken()

		client.mutex.Unlock()
	}
}

// goroutine that retrieves the latest checkin
func continousCheckin(client *restClient) {
	ticker := time.NewTicker(1 * time.Second)
	for range ticker.C {
		client.mutex.Lock()
		if client.stop {
			break
		}

		client.checkin()

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

func (r *restClient) checkConnected() bool {
	r.mutex.Lock()
	defer r.mutex.Unlock()

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
			"status": response.StatusCode,
			"body":   response.Body,
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
			"status": response.StatusCode,
			"body":   response.Body,
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

func (r *restClient) connect() {
	if r.checkConnected() {
		return
	}

}
