package server

import (
	"net"
	"sync"
	"time"
)

// User represents a logged in user to the proxy server. Contains info if they are hosting a game or linked to a game or neither
type User struct {
	Address     net.Addr
	Username    string
	Linkedto    uint64
	Hosting     uint64
	LastTokenAt time.Time
}

// Lobby represents a lobby hosted by a user on the proxy server.
type Lobby struct {
	ID           uint64
	Name         string
	HostUsername string
	Password     string
}

// Matchmaker holds the current users logged in and their status
type Matchmaker struct {
	users   map[string]User
	lobbies map[uint64]Lobby
	mutex   sync.Mutex
}
