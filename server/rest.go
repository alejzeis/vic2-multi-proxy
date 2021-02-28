package server

import (
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"net/http"
	"strconv"
	"time"

	"github.com/alejzeis/vic2-multi-proxy/common"

	"github.com/dgrijalva/jwt-go"
	"github.com/gorilla/mux"
	log "github.com/sirupsen/logrus"
	"gopkg.in/ini.v1"
)

type restServer struct {
	infoResponseJSON []byte // Cached bytes of the JSON for the /info response

	matchmaker *Matchmaker
	relay      RelayServer

	wsUpgrader *websocket.Upgrader

	secret         []byte
	lobbyIdCounter uint64
	userIdCounter  uint64
}

func verifyToken(tokenStr string, secret []byte) (bool, *common.AuthTokenCustomClaims) {
	decodedToken, err := jwt.ParseWithClaims(tokenStr, &common.AuthTokenCustomClaims{}, func(token *jwt.Token) (interface{}, error) {
		// Don't forget to validate the alg is what you expect:
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}

		return secret, nil
	})

	if err != nil {
		log.WithError(err).Warn("Failed to decode token, probably invalid signature")
		return false, nil
	}

	if claims, ok := decodedToken.Claims.(*common.AuthTokenCustomClaims); ok && decodedToken.Valid {
		if time.Now().After(time.Unix(claims.ExpiresAt, 0)) {
			return false, nil
		}

		return true, claims
	}

	return false, nil
}

// NOT thread safe
func (rs *restServer) authMethodVerification(tokenStr string, w http.ResponseWriter) (bool, User) {
	// Verify their JWT is valid
	valid, tokenClaims := verifyToken(tokenStr, rs.secret)
	if !valid {
		w.WriteHeader(http.StatusForbidden)
		return false, User{}
	}

	// Verify we have their user in the matchmaker map
	user, exists := rs.matchmaker.users[tokenClaims.UserID]
	if !exists {
		w.WriteHeader(http.StatusNotFound)
		return false, User{}
	}

	return true, user
}

// Executes every 20 seconds and searches for users that haven't renewed their tokens, and deletes them so they can relogin again
func (rs *restServer) findExpiredUserSessions() {
	ticker := time.NewTicker(20 * time.Second) // Tick every 20 seconds
	for range ticker.C {
		rs.matchmaker.mutex.Lock()

		for _, user := range rs.matchmaker.users {
			expireTime := user.LastTokenAt.Add(2 * time.Minute)
			if time.Now().After(expireTime) {
				// Expire time is past the current time, their token has expired, time to delete the user
				if user.Hosting > 0 {
					// They are hosting a lobby, need to delete that too
					delete(rs.matchmaker.lobbies, user.Hosting)
				}
				delete(rs.matchmaker.users, user.ID)

				log.WithFields(log.Fields{
					"username":   user.Username,
					"hosting":    user.Hosting,
					"linked":     user.Linkedto,
					"expireTime": expireTime,
				}).Debug("Removed expired user session")
			}
		}

		rs.matchmaker.mutex.Unlock()
	}
}

// StartControlServer begins handling HTTP requests for the REST API, called by main function
func StartControlServer(config *ini.File, mm *Matchmaker, relay RelayServer) {
	log.Info("Starting REST API HTTP Server...")

	server := new(restServer)
	server.lobbyIdCounter = 1 // Lobby IDs start at 1, since 0 represents not linked to a lobby
	server.infoResponseJSON, _ = json.Marshal(common.InfoResponse{Software: common.SoftwareName, Version: common.SoftwareVersion, API: common.APIVersion})
	server.matchmaker = mm
	server.relay = relay

	server.wsUpgrader = new(websocket.Upgrader)
	server.wsUpgrader.ReadBufferSize = 2048
	server.wsUpgrader.WriteBufferSize = 2048

	router := mux.NewRouter().StrictSlash(true)
	router.HandleFunc("/relay", server.handleWebsocketUpgrade)
	router.HandleFunc("/info", server.handleInfo).Methods("GET")
	router.HandleFunc("/login/{username}", server.handleLogin).Methods("POST")
	router.HandleFunc("/logout/{token}", server.handleLogout).Methods("GET")
	router.HandleFunc("/renew/{token}", server.handleRenew).Methods("GET")
	router.HandleFunc("/checkin/{token}", server.handleCheckin).Methods("GET")
	router.HandleFunc("/host/{token}", server.handleUpdateHostStatus).Methods("PUT")
	router.HandleFunc("/host/{token}", server.handleDeleteHostStatus).Methods("DELETE")
	router.HandleFunc("/link/{token}/{lobbyid}", server.handleUpdateLinkStatus).Methods("PUT")

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

	server.secret = []byte(secretKey.String())

	server.relay.initialize(server.secret, mm)

	go server.findExpiredUserSessions()

	log.WithError(http.ListenAndServe(":"+strconv.Itoa(port), router)).WithField("port", port).Error("Failed to start listening")
}

// This handles the endpoint /relay, which then upgrades the connection to a websocket connection and passes it to the relay server
func (rs *restServer) handleWebsocketUpgrade(w http.ResponseWriter, r *http.Request) {
	ws, err := rs.wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.WithError(err).WithField("address", r.RemoteAddr).Warn("Failed to upgrade connection to websocket connection")
	} else {
		log.WithField("address", r.RemoteAddr).Debug("Websocket connection upgraded, sent to relay")
		rs.relay.registerNewConnection(ws)
	}
}

// Returns server information such as the software version and REST API version
func (rs *restServer) handleInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(rs.infoResponseJSON)
}

// Handles a login from a client and issues a JWT with their username
// HTTP Responses:
//   - 400 Bad Request: Client omitted the username variable in the path (/login/[username])
//   - 409 Conflict: There's already a valid token that exists for the client (already logged in)
//   - 500 Internal Server Error: Failed to encode the JWT
//   - 201 Created: Successfully created user entry, and returns a JWT for use with other REST methods
func (rs *restServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	// Lock the mutex so we don't have a race condition while reading/adding stuff to the users map
	rs.matchmaker.mutex.Lock()
	defer rs.matchmaker.mutex.Unlock()

	vars := mux.Vars(r)
	username := vars["username"]

	log.Info(r.RemoteAddr)

	if username == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	exists := false
	for _, user := range rs.matchmaker.users {
		if user.Username == username {
			exists = true
			break
		}
	}
	if exists {
		w.WriteHeader(http.StatusConflict)
		return
	}

	t := jwt.NewWithClaims(jwt.SigningMethodHS384, jwt.MapClaims{
		"iss": common.SoftwareName,
		"sub": username,
		"uid": rs.userIdCounter,
		"iat": time.Now().Unix(),
		"exp": time.Now().Local().Add(time.Minute * 2).Unix(),
	})

	signedToken, err := t.SignedString(rs.secret)
	if err != nil {
		log.WithField("username", username).WithError(err).Error("Failed to encode JWT for a login request.")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	rs.matchmaker.users[rs.userIdCounter] = User{
		ID:          rs.userIdCounter,
		Address:     nil,
		Username:    username,
		Linkedto:    0, // No Lobby linked to
		Hosting:     0, // 0, no lobby hosting
		LastTokenAt: time.Now(),
	}

	log.WithFields(log.Fields{
		"username": username,
		"userID":   rs.userIdCounter,
		"address":  r.RemoteAddr,
	}).Info("New Login")
	rs.userIdCounter++

	w.WriteHeader(http.StatusCreated)
	fmt.Fprint(w, signedToken)
}

// Called by any client with their JWT to logout of their session on the proxy server.
// HTTP Responses:
//   - 400 Bad Request: Client omitted the token variable in the path (/logout/[token])
//   - 403 Forbidden: JWT wasn't valid
//   - 404 Not Found: Username wasn't found in the program's map
//   - 204 No Content: Successfully logged out
func (rs *restServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Lock the mutex so we don't have a race condition while reading/adding stuff to the users map
	rs.matchmaker.mutex.Lock()
	defer rs.matchmaker.mutex.Unlock()

	vars := mux.Vars(r)
	token := vars["token"]

	if token == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	success, user := rs.authMethodVerification(token, w)
	if !success {
		return
	}

	if user.Hosting > 0 {
		// User is hosting a lobby, need to remove the lobby from the server

		rs.relay.onUserStopHosting(user, rs.matchmaker.lobbies[user.Hosting]) // Notify the relay server the user is stopping hosting

		// First we need to find all the users linked to it, and update their struct to show they aren't linked anymore
		for key, val := range rs.matchmaker.users {
			if val.Linkedto == user.Hosting {
				val.Linkedto = 0
				rs.matchmaker.users[key] = val
			}
		}
		// Then we delete the lobby struct
		delete(rs.matchmaker.lobbies, user.Hosting)
	}

	log.WithFields(log.Fields{
		"username": user.Username,
		"userID":   user.ID,
		"address":  r.RemoteAddr,
	}).Info("New Logout")

	delete(rs.matchmaker.users, user.ID)
	w.WriteHeader(http.StatusNoContent)
}

// Used by a client to renew their authentication token (JWT), should be called every minute or so
// HTTP Responses:
//   - 400 Bad Request: Client omitted the token variable in the path (/renew/[token])
//   - 403 Forbidden: JWT wasn't valid
//   - 404 Not Found: Username wasn't found in the program's map
//   - 500 Internal Server Error: Failed to encode the JWT
//   - 200 OK: Successfully created new token, returns new JWT
func (rs *restServer) handleRenew(w http.ResponseWriter, r *http.Request) {
	// Lock the mutex so we don't have a race condition while reading/adding stuff to the users map
	rs.matchmaker.mutex.Lock()
	defer rs.matchmaker.mutex.Unlock()

	vars := mux.Vars(r)
	token := vars["token"]
	// Verify REST parameters
	if token == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Verify token
	success, user := rs.authMethodVerification(token, w)
	if !success {
		return
	}

	issuedTime := time.Now()
	t := jwt.NewWithClaims(jwt.SigningMethodHS384, jwt.MapClaims{
		"iss": common.SoftwareName,
		"sub": user.Username,
		"uid": user.ID,
		"iat": issuedTime.Unix(),
		"exp": issuedTime.Local().Add(time.Minute * 2).Unix(),
	})

	signedToken, err := t.SignedString(rs.secret)
	if err != nil {
		log.WithFields(log.Fields{
			"username": user.Username,
			"userID":   user.ID,
		}).WithError(err).Error("Failed to encode JWT for renewal.")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	user.LastTokenAt = issuedTime
	rs.matchmaker.users[user.ID] = user

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, signedToken)
}

// Called by any client every 2 seconds with their JWT, returns a list of lobbies on the server and their client's status (hosting, linked, or neither)
// HTTP Responses:
//   - 400 Bad Request: Client omitted the token variable in the path (/checkin/[token])
//   - 403 Forbidden: JWT wasn't valid
//   - 404 Not Found: Username wasn't found in the program's map
//   - 200 OK: Success, returns checkinResponse struct (JSON)
func (rs *restServer) handleCheckin(w http.ResponseWriter, r *http.Request) {
	// Lock the mutex so we don't have a race condition while reading/adding stuff to the users map
	rs.matchmaker.mutex.Lock()
	defer rs.matchmaker.mutex.Unlock()

	vars := mux.Vars(r)
	token := vars["token"]
	// Verify REST parameters
	if token == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	success, user := rs.authMethodVerification(token, w)
	if !success {
		return
	}

	lobbyMap := make(map[string]common.RestLobby)
	for _, val := range rs.matchmaker.lobbies {
		lobbyMap[strconv.FormatUint(val.ID, 10)] = common.RestLobby{Name: val.Name, Host: rs.matchmaker.users[val.HostUserID].Username}
	}

	var isHosting bool
	if user.Hosting > 0 {
		isHosting = true
	} else {
		isHosting = false
	}

	data, err := json.Marshal(common.CheckinResponse{
		Lobbies:     lobbyMap,
		Hosting:     isHosting,
		LinkedLobby: user.Linkedto,
	})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		log.WithError(err).WithFields(log.Fields{
			"user":     user.Username,
			"userID":   user.ID,
			"address":  r.RemoteAddr,
			"hosting":  user.Hosting,
			"linkedTo": user.Linkedto,
		}).Error("Failed to encode response json for /checkin")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
	log.Debug("Checkin Complete")
}

// Called by any client when they want to host a lobby for linking on the proxy
// HTTP PUT Method
// HTTP Responses:
//   - 400 Bad Request: Client omitted the token variable in the path (/host/[token])
//   - 403 Forbidden: JWT wasn't valid
//   - 404 Not Found: Username wasn't found in the program's map
//   - 409 Conflict: User is linked to a lobby, or is already hosting
//   - 204 No content: Successfully hosting lobby
func (rs *restServer) handleUpdateHostStatus(w http.ResponseWriter, r *http.Request) {
	// Lock the mutex so we don't have a race condition while reading/adding stuff to the users map
	rs.matchmaker.mutex.Lock()
	defer rs.matchmaker.mutex.Unlock()

	vars := mux.Vars(r)
	token := vars["token"]

	// Verify REST parameters
	if token == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	success, user := rs.authMethodVerification(token, w)
	if !success {
		return
	}

	// Make sure they aren't already hosting a lobby or are linked to one
	if user.Hosting > 0 || user.Linkedto > 0 {
		w.WriteHeader(http.StatusConflict)
		return
	}

	err := r.ParseForm()
	if err != nil {
		log.WithError(err).WithFields(log.Fields{
			"username": user.Username,
			"userID":   user.ID,
			"address":  r.RemoteAddr,
		}).Warning("Failed to parse form for /host API endpoint")
	}
	if r.FormValue("name") == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	lobby := Lobby{
		ID:         rs.lobbyIdCounter,
		Name:       r.FormValue("name"),
		Password:   r.FormValue("password"),
		HostUserID: user.ID,
	}

	rs.matchmaker.lobbies[rs.lobbyIdCounter] = lobby
	user.Hosting = rs.lobbyIdCounter
	rs.matchmaker.users[user.ID] = user
	rs.lobbyIdCounter++

	log.WithFields(log.Fields{
		"hostedBy":   user.Username,
		"hostUserID": user.ID,
		"id":         lobby.ID,
		"name":       lobby.Name,
	}).Info("New lobby created")

	rs.relay.onUserStartHosting(user, lobby)

	w.WriteHeader(http.StatusNoContent)
}

// Called by any client when they want to stop hosting a lobby.
// HTTP DELETE Method
// HTTP Responses:
//   - 400 Bad Request: Client omitted the token variable in the path (/host/[token])
//   - 403 Forbidden: JWT wasn't valid
//   - 404 Not Found: Username wasn't found in the program's map, or user isn't hosting a lobby
//   - 204 No content: Successfully deleted lobby
func (rs *restServer) handleDeleteHostStatus(w http.ResponseWriter, r *http.Request) {
	// Lock the mutex so we don't have a race condition while reading/adding stuff to the users map
	rs.matchmaker.mutex.Lock()
	defer rs.matchmaker.mutex.Unlock()

	vars := mux.Vars(r)
	token := vars["token"]

	// Verify REST parameters
	if token == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	success, user := rs.authMethodVerification(token, w)
	if !success {
		return
	}

	// Check if they are hosting
	if user.Hosting < 1 {
		w.WriteHeader(http.StatusNotFound) // User isn't hosting
		return
	}

	rs.relay.onUserStopHosting(user, rs.matchmaker.lobbies[user.Hosting]) // Notify relay server that the user isn't hosting anymore

	name := rs.matchmaker.lobbies[user.Hosting].Name
	id := rs.matchmaker.lobbies[user.Hosting].ID
	delete(rs.matchmaker.lobbies, user.Hosting)

	user.Hosting = 0
	rs.matchmaker.users[user.ID] = user

	log.WithFields(log.Fields{
		"hostedBy":   user.Username,
		"hostUserID": user.ID,
		"id":         id,
		"name":       name,
	}).Info("Lobby deleted")

	w.WriteHeader(http.StatusNoContent)
}

// Called by any client when they want to link to a lobby, or unlink from one.
// Note: The client MUST NOT be hosting a lobby at the same time, returns 409 if client is hosting
// HTTP Responses:
//   - 400 Bad Request: Client omitted a variable in the path (/link/[token]/[lobbyid]), or lobbyid is not a greater than zero integer
//   - 403 Forbidden: JWT wasn't valid
//   - 404 Not Found: No lobby was found with the matching ID
//   - 409 Conflict: User is hosting a lobby, can't link while hosting
//   - 423 Locked: Lobby the client wants to link to is password-protected (TODO)
//   - 204 No content: Successfully linked to lobby
func (rs *restServer) handleUpdateLinkStatus(w http.ResponseWriter, r *http.Request) {
	// Lock the mutex so we don't have a race condition while reading/adding stuff to the users map
	rs.matchmaker.mutex.Lock()
	defer rs.matchmaker.mutex.Unlock()

	vars := mux.Vars(r)
	token := vars["token"]
	lobbyid, err := strconv.ParseUint(vars["lobbyid"], 10, 64)

	// Verify REST parameters
	if err != nil || token == "" || lobbyid < 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	success, user := rs.authMethodVerification(token, w)
	if !success {
		return
	}

	if user.Hosting > 0 { // Check to see if they are hosting a lobby
		w.WriteHeader(http.StatusConflict)
		return
	}

	if lobbyid == 0 {
		// They want to unlink from their lobby

		rs.relay.onUserUnlinkLobby(user, rs.matchmaker.lobbies[user.Linkedto]) // Notify relay server they aren't linked anymore

		oldLobby := user.Linkedto
		user.Linkedto = 0
		rs.matchmaker.users[user.ID] = user
		w.WriteHeader(http.StatusNoContent)

		log.WithFields(log.Fields{
			"username": user.Username,
			"userID":   user.ID,
			"lobbyId":  oldLobby,
		}).Info("Unlinked from lobby")
		return
	}

	// Check to make sure the lobby actually exists
	lobbyIdExists := false
	for key := range rs.matchmaker.lobbies {
		if key == lobbyid {
			lobbyIdExists = true
			break
		}
	}

	if !lobbyIdExists {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// TODO: PASSWORD PROTECTED LOBBIES SUPPORT
	if rs.matchmaker.lobbies[lobbyid].Password != "" {
		w.WriteHeader(http.StatusLocked)
		return
	}

	user.Linkedto = lobbyid
	rs.matchmaker.users[user.ID] = user

	log.WithFields(log.Fields{
		"username": user.Username,
		"userID":   user.ID,
		"lobbyId":  user.Linkedto,
	}).Info("Linked to lobby")

	rs.relay.onUserLinkLobby(user, rs.matchmaker.lobbies[user.Linkedto]) // Notify relay server they are linked to a lobby

	w.WriteHeader(http.StatusNoContent)
}
