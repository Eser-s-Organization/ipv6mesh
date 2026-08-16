package control

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

type RoomJoinRequest struct {
	DisplayName   string
	PublicKey     string
	Platform      string
	ClientVersion string
}

func (client *Client) CreateRoom(ctx context.Context, name, pool, token string) (Network, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(pool) == "" {
		return Network{}, ErrValidation
	}
	var response networkWireResponse
	if err := client.doJSON(ctx, http.MethodPost, "/v1/room", token, map[string]string{
		"name":      name,
		"ipv4_pool": pool,
	}, &response); err != nil {
		return Network{}, err
	}
	if response.ID == "" || strings.TrimSpace(response.IPv4Pool) == "" && strings.TrimSpace(response.Pool) == "" {
		return Network{}, fmt.Errorf("%w: room response is incomplete", ErrControlInvalidResponse)
	}
	return networkFromWire(response), nil
}

func (client *Client) JoinRoom(ctx context.Context, request RoomJoinRequest) (EnrollmentResult, error) {
	if strings.TrimSpace(request.DisplayName) == "" || strings.TrimSpace(request.PublicKey) == "" ||
		strings.TrimSpace(request.Platform) == "" || strings.TrimSpace(request.ClientVersion) == "" {
		return EnrollmentResult{}, ErrValidation
	}
	var response enrollmentWireResponse
	if err := client.doJSONNoAuth(ctx, http.MethodPost, "/v1/room/join", map[string]string{
		"public_key":     request.PublicKey,
		"display_name":   request.DisplayName,
		"platform":       request.Platform,
		"client_version": request.ClientVersion,
	}, &response); err != nil {
		return EnrollmentResult{}, err
	}
	return enrollmentResultFromWire(response)
}
