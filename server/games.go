package server

import (
	"net"
	"sync"
	"time"
)

// User represents a logged in user to the proxy server. Contains info if they are hosting a game or linked to a game or neither
type User struct {
	ID          uint64
	Address     net.Addr
	Username    string
	Linkedto    uint64
	Hosting     uint64
	LastTokenAt time.Time
}

// Lobby represents a lobby hosted by a user on the proxy server.
type Lobby struct {
	ID         uint64
	Name       string
	HostUserID uint64
	Password   string
}

// Matchmaker is a structure that allows keeping track of users and lobbies.
type Matchmaker struct {
	// Map of user Ids matched to User structs
	users map[uint64]User
	// Map of lobby Ids matched to Lobby structs
	lobbies map[uint64]Lobby
	mutex   *sync.Mutex
}

func (mm *Matchmaker) Init() {
	mm.users = make(map[uint64]User)
	mm.lobbies = make(map[uint64]Lobby)
	mm.mutex = &sync.Mutex{}
}
