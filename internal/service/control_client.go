package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
)

var ErrControlClient = errors.New("control client is unavailable")

type HTTPControlClient struct {
	client        *control.Client
	nodeID        string
	platform      string
	clientVersion string

	mu           sync.RWMutex
	networkID    string
	sessionToken string
}

func NewHTTPControlClient(client *control.Client, nodeID, platform, clientVersion string) *HTTPControlClient {
	if strings.TrimSpace(platform) == "" {
		platform = "windows"
	}
	if strings.TrimSpace(clientVersion) == "" {
		clientVersion = "dev"
	}
	return &HTTPControlClient{client: client, nodeID: nodeID, platform: platform, clientVersion: clientVersion}
}

func (client *HTTPControlClient) Join(ctx context.Context, request JoinRequest) (JoinResult, error) {
	if client == nil || client.client == nil {
		return JoinResult{}, ErrControlClient
	}
	nodeID := strings.TrimSpace(client.nodeID)
	if nodeID == "" {
		nodeID = stableNodeID(request.PublicKey)
	}
	result, err := client.client.Join(ctx, control.JoinRequest{Invite: request.Invite, NodeID: nodeID, DisplayName: request.DisplayName, PublicKey: request.PublicKey, Platform: client.platform, ClientVersion: client.clientVersion})
	if err != nil {
		return JoinResult{}, err
	}
	return client.rememberEnrollment(result)
}

func (client *HTTPControlClient) JoinRoom(ctx context.Context, request JoinRequest) (JoinResult, error) {
	if client == nil || client.client == nil {
		return JoinResult{}, ErrControlClient
	}
	result, err := client.client.JoinRoom(ctx, control.RoomJoinRequest{
		DisplayName:   request.DisplayName,
		PublicKey:     request.PublicKey,
		Platform:      client.platform,
		ClientVersion: client.clientVersion,
	})
	if err != nil {
		return JoinResult{}, err
	}
	return client.rememberEnrollment(result)
}

func (client *HTTPControlClient) rememberEnrollment(result control.EnrollmentResult) (JoinResult, error) {
	joined := JoinResult{DisplayName: result.Node.DisplayName, NetworkID: result.Network.ID, VirtualIPv4: result.Membership.VirtualIPv4.String(), ConfigGeneration: result.Network.ConfigVersion}
	client.mu.Lock()
	client.nodeID = result.Node.ID
	client.networkID = result.Network.ID
	client.sessionToken = result.SessionToken
	client.mu.Unlock()
	return joined, nil
}

func (client *HTTPControlClient) Leave(ctx context.Context, networkID string) error {
	if client == nil || client.client == nil {
		return ErrControlClient
	}
	client.mu.RLock()
	nodeID, sessionToken, joinedNetworkID := client.nodeID, client.sessionToken, client.networkID
	client.mu.RUnlock()
	if joinedNetworkID != networkID || nodeID == "" || sessionToken == "" {
		return errors.New("control client is not joined to the requested network")
	}
	if err := client.client.Leave(ctx, networkID, nodeID, sessionToken); err != nil {
		return err
	}
	client.mu.Lock()
	client.networkID = ""
	client.sessionToken = ""
	client.mu.Unlock()
	return nil
}

func (client *HTTPControlClient) Snapshot(ctx context.Context, networkID string) (control.NetworkSnapshot, error) {
	if client == nil || client.client == nil {
		return control.NetworkSnapshot{}, ErrControlClient
	}
	client.mu.RLock()
	sessionToken := client.sessionToken
	client.mu.RUnlock()
	return client.client.Snapshot(ctx, networkID, sessionToken)
}

func (client *HTTPControlClient) Watch(ctx context.Context, networkID string, onSnapshot func(control.NetworkSnapshot) error) error {
	if client == nil || client.client == nil {
		return ErrControlClient
	}
	client.mu.RLock()
	sessionToken := client.sessionToken
	client.mu.RUnlock()
	return client.client.Watch(ctx, networkID, sessionToken, onSnapshot)
}

func (client *HTTPControlClient) Heartbeat(ctx context.Context, endpoints []control.EndpointCandidate) error {
	if client == nil || client.client == nil {
		return ErrControlClient
	}
	client.mu.RLock()
	networkID, nodeID, sessionToken, version := client.networkID, client.nodeID, client.sessionToken, client.clientVersion
	client.mu.RUnlock()
	return client.client.Heartbeat(ctx, networkID, nodeID, sessionToken, version, endpoints)
}

func stableNodeID(publicKey string) string {
	digest := sha256.Sum256([]byte(publicKey))
	return "node-" + hex.EncodeToString(digest[:8])
}
