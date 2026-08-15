package main

import (
	"encoding/json"
	"io"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
)

type networkOutput struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	IPv4Pool      string `json:"ipv4_pool"`
	OwnerID       string `json:"owner_id"`
	ConfigVersion int64  `json:"config_version"`
}

func writeNetworkOutput(writer io.Writer, network control.Network) error {
	return writeJSONOutput(writer, networkOutput{ID: network.ID, Name: network.Name, IPv4Pool: network.IPv4Pool, OwnerID: network.OwnerID, ConfigVersion: network.ConfigVersion})
}

func writeInviteOutput(writer io.Writer, invite control.InviteResult) error {
	return writeJSONOutput(writer, invite)
}

func writeJSONOutput(writer io.Writer, value any) error {
	return json.NewEncoder(writer).Encode(value)
}
