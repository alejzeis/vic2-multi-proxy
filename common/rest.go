package common

// SoftwareName is the name of this software
const SoftwareName = "vic2-multi-proxy"

// SoftwareVersion is the version of this software
const SoftwareVersion = "v1.0.0-alpha"

// APIVersion is the version of the REST API implemented in this file
const APIVersion uint = 1

// InfoResponse is the JSON response to the /info REST method
type InfoResponse struct {
	Software string `json:"software"`
	Version  string `json:"version"`
	API      uint   `json:"apiVersion"`
}

// CheckinResponse is the JSON response to the /checkin REST method
type CheckinResponse struct {
	Lobbies     map[string]RestLobby `json:"lobbies"`
	Hosting     bool                 `json:"hosting"`
	LinkedLobby uint64               `json:"linkedTo"`
}

// RestLobby represents a Lobby in the lobbies dictionary in the CheckinResponse struct
type RestLobby struct {
	Name string `json:"name"`
	Host string `json:"host"`
}
