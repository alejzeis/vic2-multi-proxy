package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/gorilla/mux"
	log "github.com/sirupsen/logrus"
	"gopkg.in/ini.v1"
)

// APIVersion is the version of the REST API implemented in this file
const APIVersion uint = 1

// infoResponse is the JSON response to the /info REST method
type infoResponse struct {
	Software string `json:"software"`
	Version  string `json:"version"`
	API      uint   `json:"apiVersion"`
}

// checkinResponse is the JSON response to the /checkin REST method
type checkinResponse struct {
	Lobbies     map[string]restLobby `json:"lobbies"`
	Hosting     bool                 `json:"hosting"`
	LinkedLobby uint64               `json:"linkedTo"`
}

type restLobby struct {
	Name string `json:"name"`
	Host string `json:"host"`
}

var infoResponseJSON []byte // Cached bytes of the JSON for the /info response

var matchmaker *Matchmaker
var secret []byte // HMAC secret used for signing JWTs

func verifyToken(tokenStr string) (bool, string) {
	decodedToken, err := jwt.ParseWithClaims(tokenStr, &jwt.StandardClaims{}, func(token *jwt.Token) (interface{}, error) {
		// Don't forget to validate the alg is what you expect:
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("Unexpected signing method: %v", token.Header["alg"])
		}

		return secret, nil
	})

	if err != nil {
		log.WithError(err).Warn("Failed to decode token, probably invalid signature")
		return false, ""
	}

	if claims, ok := decodedToken.Claims.(*jwt.StandardClaims); ok && decodedToken.Valid {
		if time.Now().After(time.Unix(claims.ExpiresAt, 0)) {
			return false, ""
		}

		return true, claims.Subject
	}

	return false, ""
}

func authMethodVerification(tokenStr string, w http.ResponseWriter) (bool, User) {
	// Verify their JWT is valid
	valid, username := verifyToken(tokenStr)
	if !valid {
		w.WriteHeader(http.StatusForbidden)
		return false, User{}
	}

	// Verify we have their user in the matchmaker map
	user, exists := matchmaker.users[username]
	if !exists {
		w.WriteHeader(http.StatusNotFound)
		return false, User{}
	}

	return true, user
}

// StartControlServer begins handling HTTP requests for the REST API, called by main function
func StartControlServer(config *ini.File, mm *Matchmaker) {
	log.Info("Starting REST API HTTP Server...")

	infoResponseJSON, _ = json.Marshal(infoResponse{SoftwareName, SoftwareVersion, APIVersion})

	mm.users = make(map[string]User)
	matchmaker = mm

	router := mux.NewRouter().StrictSlash(true)
	router.HandleFunc("/info", handleInfo).Methods("GET")
	router.HandleFunc("/login/{username}", handleLogin) //.Methods("POST")
	router.HandleFunc("/logout/{token}", handleLogout).Methods("GET")
	router.HandleFunc("/checkin/{token}", handleCheckin).Methods("GET")
	router.HandleFunc("/host/{token}/{lobbyinfo}", handleUpdateHostStatus) //.Methods("PUT")
	router.HandleFunc("/link/{token}/{lobbyid}", handleUpdateLinkStatus)   //.Methods("PUT")

	portKey, err := config.Section("server").GetKey("port")
	if err != nil {
		log.WithError(err).Error("Failed to get server port from configuration file.")
		panic(err)
	}
	port, err2 := portKey.Int()
	if err2 != nil {
		log.WithError(err).Error("Failed to get server port as integer from configuration file.")
		panic(err)
	}

	secretKey, err := config.Section("server").GetKey("secret")
	if err != nil {
		log.WithError(err).Error("Failed to get server secret from configuration file.")
		panic(err)
	}

	secret = []byte(secretKey.String())

	log.WithError(http.ListenAndServe(":"+strconv.Itoa(port), router)).WithField("port", port).Error("Failed to start listening")
}

// Returns server information such as the software version and REST API version
func handleInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(infoResponseJSON)
}

// Handles a login from a client and issues a JWT with their username
func handleLogin(w http.ResponseWriter, r *http.Request) {
	// Lock the mutex so we don't have a race condition while reading/adding stuff to the users map
	matchmaker.mutex.Lock()
	defer matchmaker.mutex.Unlock()

	vars := mux.Vars(r)
	username := vars["username"]

	log.Info(r.RemoteAddr)

	if username == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	_, exists := matchmaker.users[username]
	if exists {
		w.WriteHeader(http.StatusConflict)
		return
	}

	t := jwt.NewWithClaims(jwt.SigningMethodHS384, jwt.MapClaims{
		"iss": "vic2multi-proxy",
		"sub": username,
		"iat": time.Now().Unix(),
		"exp": time.Now().Local().Add(time.Minute * 2).Unix(),
	})

	signedToken, err := t.SignedString(secret)
	if err != nil {
		log.WithField("username", username).WithError(err).Error("Failed to encode JWT for a login request.")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	matchmaker.users[username] = User{
		Address:     nil,
		Username:    username,
		Linkedto:    0, // No Lobby linked to
		Hosting:     0, // 0, no lobby hosting
		LastTokenAt: time.Now(),
	}

	log.WithFields(log.Fields{
		"username": username,
		"address":  r.RemoteAddr,
	}).Info("New Login")

	w.WriteHeader(http.StatusCreated)
	fmt.Fprint(w, signedToken)
}

// Called by any client with their JWT to logout of their session on the proxy server.
func handleLogout(w http.ResponseWriter, r *http.Request) {
	// Lock the mutex so we don't have a race condition while reading/adding stuff to the users map
	matchmaker.mutex.Lock()
	defer matchmaker.mutex.Unlock()

	vars := mux.Vars(r)
	token := vars["token"]

	if token == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	valid, username := verifyToken(token)
	if !valid {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	user, exists := matchmaker.users[username]
	if !exists {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if user.Hosting > 0 {
		// User is hosting a lobby, need to remove the lobby from the server
		// First we need to find all the users linked to it, and update their struct to show they aren't linked anymore
		for key, val := range matchmaker.users {
			if val.Linkedto == user.Hosting {
				val.Linkedto = 0
				matchmaker.users[key] = val
			}
		}
		// Then we delete the lobby struct
		delete(matchmaker.lobbies, user.Hosting)
	}

	log.WithFields(log.Fields{
		"username": username,
		"address":  r.RemoteAddr,
	}).Info("New Logout")

	delete(matchmaker.users, username)
	w.WriteHeader(http.StatusOK)
}

// Called by any client every 2 seconds with their JWT, returns a list of lobbies on the server and their client's status (hosting, linked, or neither)
func handleCheckin(w http.ResponseWriter, r *http.Request) {
	// Lock the mutex so we don't have a race condition while reading/adding stuff to the users map
	matchmaker.mutex.Lock()
	defer matchmaker.mutex.Unlock()

	vars := mux.Vars(r)
	token := vars["token"]
	// Verify REST parameters
	if token == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	success, user := authMethodVerification(token, w)
	if !success {
		return
	}

	lobbyMap := make(map[string]restLobby)
	for _, val := range matchmaker.lobbies {
		lobbyMap[strconv.FormatUint(val.ID, 10)] = restLobby{val.Name, val.HostUsername}
	}

	var isHosting bool
	if user.Hosting > 0 {
		isHosting = true
	} else {
		isHosting = false
	}

	data, err := json.Marshal(checkinResponse{
		Lobbies:     lobbyMap,
		Hosting:     isHosting,
		LinkedLobby: user.Linkedto,
	})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.WithError(err).WithFields(log.Fields{
			"user":     user.Username,
			"address":  r.RemoteAddr,
			"hosting":  user.Hosting,
			"linkedTo": user.Linkedto,
		}).Error("Failed to encode response json for /checkin")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// Called by any client when they want to host a lobby for linking on the proxy, or unhost their lobby.
func handleUpdateHostStatus(w http.ResponseWriter, r *http.Request) {
	// Lock the mutex so we don't have a race condition while reading/adding stuff to the users map
	matchmaker.mutex.Lock()
	defer matchmaker.mutex.Unlock()

	vars := mux.Vars(r)
	token := vars["token"]
	lobbyinfo := vars["lobbyinfo"]

	// Verify REST parameters
	if token == "" || lobbyinfo == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	success, _ := authMethodVerification(token, w)
	if !success {
		return
	}
}

// Called by any client when they want to link to a lobby, or unlink from one.
// Note: The client MUST NOT be hosting a lobby at the same time.
func handleUpdateLinkStatus(w http.ResponseWriter, r *http.Request) {
	// Lock the mutex so we don't have a race condition while reading/adding stuff to the users map
	matchmaker.mutex.Lock()
	defer matchmaker.mutex.Unlock()

	vars := mux.Vars(r)
	token := vars["token"]
	lobbyid := vars["lobbyid"]

	// Verify REST parameters
	if token == "" || lobbyid == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	success, _ := authMethodVerification(token, w)
	if !success {
		return
	}
}
