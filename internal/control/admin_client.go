package control

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type InviteResult struct {
	InviteID  string    `json:"invite_id"`
	NetworkID string    `json:"network_id"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (client *Client) CreateNetwork(ctx context.Context, name, pool, token string) (Network, error) {
	return client.CreateNetworkWithID(ctx, name, pool, "", token)
}

// CreateNetworkWithID creates a network and optionally requests a specific
// caller-generated ID. An empty networkID preserves the control plane's
// default cryptographically random ID generation.
func (client *Client) CreateNetworkWithID(ctx context.Context, name, pool, networkID, token string) (Network, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(pool) == "" {
		return Network{}, ErrValidation
	}
	body := map[string]string{"name": name, "pool": pool}
	if strings.TrimSpace(networkID) != "" {
		body["id"] = strings.TrimSpace(networkID)
	}
	var response networkWireResponse
	if err := client.doJSON(ctx, http.MethodPost, "/v1/networks", token, body, &response); err != nil {
		return Network{}, err
	}
	if response.ID == "" {
		return Network{}, fmt.Errorf("%w: network response is incomplete", ErrControlInvalidResponse)
	}
	return networkFromWire(response), nil
}

func (client *Client) CreateInvite(ctx context.Context, networkID, expiresIn, token string) (InviteResult, error) {
	if strings.TrimSpace(networkID) == "" || strings.TrimSpace(expiresIn) == "" {
		return InviteResult{}, ErrValidation
	}
	var response InviteResult
	if err := client.doJSON(ctx, http.MethodPost, "/v1/networks/"+url.PathEscape(networkID)+"/invites", token, map[string]string{"expires_in": expiresIn}, &response); err != nil {
		return InviteResult{}, err
	}
	if response.InviteID == "" || response.NetworkID != networkID || response.Token == "" {
		return InviteResult{}, fmt.Errorf("%w: invite response is incomplete", ErrControlInvalidResponse)
	}
	return response, nil
}
