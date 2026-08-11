package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Eser-s-Organization/ipv6mesh/internal/auth"
	"github.com/gorilla/websocket"
)

var (
	ErrInvalidClient          = errors.New("invalid control-plane client")
	ErrControlHTTP            = errors.New("control-plane HTTP request failed")
	ErrControlUnauthorized    = errors.New("control-plane authentication failed")
	ErrControlForbidden       = errors.New("control-plane authorization failed")
	ErrControlNotFound        = errors.New("control-plane resource not found")
	ErrControlConflict        = errors.New("control-plane conflict")
	ErrControlUnavailable     = errors.New("control-plane unavailable")
	ErrControlInvalidResponse = errors.New("invalid control-plane response")
)

const defaultControlResponseLimit int64 = 1 << 20

type HTTPError struct {
	StatusCode int
	Code       string
}

func (err *HTTPError) Error() string {
	if err == nil {
		return ErrControlHTTP.Error()
	}
	if err.Code == "" {
		return fmt.Sprintf("control-plane HTTP status %d", err.StatusCode)
	}
	return fmt.Sprintf("control-plane HTTP status %d (%s)", err.StatusCode, err.Code)
}

func (err *HTTPError) Unwrap() error { return ErrControlHTTP }

func (err *HTTPError) Is(target error) bool {
	if target == ErrControlHTTP {
		return true
	}
	switch err.StatusCode {
	case http.StatusUnauthorized:
		return target == ErrControlUnauthorized
	case http.StatusForbidden:
		return target == ErrControlForbidden
	case http.StatusNotFound:
		return target == ErrControlNotFound
	case http.StatusConflict:
		return target == ErrControlConflict
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return target == ErrControlUnavailable
	default:
		return false
	}
}

type Client struct {
	BaseURL          *url.URL
	HTTPClient       *http.Client
	WebSocketDialer  *websocket.Dialer
	Token            string
	UserAgent        string
	MaxResponseBytes int64
}

func NewClient(baseURL string) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("%w: base URL must include scheme and host", ErrInvalidClient)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return &Client{
		BaseURL:          parsed,
		HTTPClient:       &http.Client{Timeout: 15 * time.Second},
		WebSocketDialer:  websocket.DefaultDialer,
		UserAgent:        "ipv6mesh-client/0.1",
		MaxResponseBytes: defaultControlResponseLimit,
	}, nil
}

type JoinRequest struct {
	Invite        string
	NodeID        string
	DisplayName   string
	PublicKey     string
	Platform      string
	ClientVersion string
}

type EnrollmentResult struct {
	Node         Node
	Membership   Membership
	Network      Network
	Session      auth.Session
	SessionToken string
}

func (client *Client) Join(ctx context.Context, request JoinRequest) (EnrollmentResult, error) {
	var response enrollmentWireResponse
	if err := client.doJSON(ctx, http.MethodPost, "/v1/enrollments", "", map[string]string{
		"invite":         request.Invite,
		"node_id":        request.NodeID,
		"display_name":   request.DisplayName,
		"public_key":     request.PublicKey,
		"platform":       request.Platform,
		"client_version": request.ClientVersion,
	}, &response); err != nil {
		return EnrollmentResult{}, err
	}
	if response.SessionToken == "" || response.Node.ID == "" || response.Membership.NetworkID == "" || response.Network.ID == "" {
		return EnrollmentResult{}, fmt.Errorf("%w: enrollment response is incomplete", ErrControlInvalidResponse)
	}
	return EnrollmentResult{
		Node:         nodeFromWire(response.Node),
		Membership:   membershipFromWire(response.Membership),
		Network:      networkFromWire(response.Network),
		Session:      auth.Session{Subject: response.Session.Subject, NetworkID: response.Session.NetworkID, ExpiresAt: response.Session.ExpiresAt, Role: auth.RoleNode},
		SessionToken: response.SessionToken,
	}, nil
}

func (client *Client) Heartbeat(ctx context.Context, networkID, nodeID, sessionToken, clientVersion string, endpoints []EndpointCandidate) error {
	if strings.TrimSpace(networkID) == "" || strings.TrimSpace(nodeID) == "" || strings.TrimSpace(sessionToken) == "" {
		return ErrValidation
	}
	now := time.Now().UTC()
	request := heartbeatWireRequest{Endpoints: make([]heartbeatWireEndpoint, len(endpoints))}
	for index, endpoint := range endpoints {
		if endpoint.NodeID == "" {
			endpoint.NodeID = nodeID
		}
		if endpoint.ObservedAt.IsZero() {
			endpoint.ObservedAt = now
		}
		if err := ValidateEndpointCandidate(endpoint); err != nil {
			return err
		}
		request.Endpoints[index] = heartbeatWireEndpoint{
			Address:    endpoint.Address.String(),
			Port:       endpoint.Port,
			Family:     endpoint.Family,
			Interface:  endpoint.Interface,
			Priority:   endpoint.Priority,
			ObservedAt: endpoint.ObservedAt.UTC().Format(time.RFC3339Nano),
		}
	}
	if clientVersion != "" {
		request.ClientVersion = &clientVersion
	}
	return client.doJSON(ctx, http.MethodPost, "/v1/nodes/"+url.PathEscape(nodeID)+"/heartbeat", sessionToken, request, nil)
}

func (client *Client) Snapshot(ctx context.Context, networkID, sessionToken string) (NetworkSnapshot, error) {
	if strings.TrimSpace(networkID) == "" || strings.TrimSpace(sessionToken) == "" {
		return NetworkSnapshot{}, ErrValidation
	}
	var response snapshotWireResponse
	if err := client.doJSON(ctx, http.MethodGet, "/v1/networks/"+url.PathEscape(networkID)+"/snapshot", sessionToken, nil, &response); err != nil {
		return NetworkSnapshot{}, err
	}
	if response.NetworkID != networkID {
		return NetworkSnapshot{}, fmt.Errorf("%w: snapshot network does not match requested network", ErrControlInvalidResponse)
	}
	return snapshotFromWire(response)
}

func (client *Client) Events(ctx context.Context, networkID, sessionToken string) (*websocket.Conn, error) {
	if strings.TrimSpace(networkID) == "" || strings.TrimSpace(sessionToken) == "" {
		return nil, ErrValidation
	}
	if client == nil || client.BaseURL == nil || client.WebSocketDialer == nil {
		return nil, ErrInvalidClient
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	eventURL := client.resolveURL("/v1/events")
	switch eventURL.Scheme {
	case "http":
		eventURL.Scheme = "ws"
	case "https":
		eventURL.Scheme = "wss"
	case "ws", "wss":
	default:
		return nil, fmt.Errorf("%w: unsupported WebSocket scheme", ErrInvalidClient)
	}
	header := http.Header{"Authorization": []string{"Bearer " + sessionToken}}
	if client.UserAgent != "" {
		header.Set("User-Agent", client.UserAgent)
	}
	connection, response, err := client.WebSocketDialer.DialContext(ctx, eventURL.String(), header)
	if err != nil {
		if response != nil {
			defer response.Body.Close()
			return nil, &HTTPError{StatusCode: response.StatusCode}
		}
		return nil, err
	}
	return connection, nil
}

func (client *Client) doJSON(ctx context.Context, method, path, token string, payload, destination any) error {
	if client == nil || client.BaseURL == nil || client.HTTPClient == nil {
		return ErrInvalidClient
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if token == "" {
		token = client.Token
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.resolveURL(path).String(), body)
	if err != nil {
		return err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if client.UserAgent != "" {
		request.Header.Set("User-Agent", client.UserAgent)
	}
	response, err := client.HTTPClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return client.httpError(response)
	}
	if destination == nil {
		return nil
	}
	return decodeLimitedJSON(response.Body, client.responseLimit(), destination)
}

func (client *Client) httpError(response *http.Response) error {
	limit := client.responseLimit()
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return err
	}
	code := ""
	if int64(len(body)) <= limit {
		var payload struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &payload) == nil {
			code = payload.Error
		}
	}
	return &HTTPError{StatusCode: response.StatusCode, Code: code}
}

func (client *Client) resolveURL(path string) *url.URL {
	resolved := *client.BaseURL
	resolved.Path = strings.TrimRight(client.BaseURL.Path, "/") + "/" + strings.TrimLeft(path, "/")
	resolved.RawPath = ""
	resolved.RawQuery = ""
	resolved.Fragment = ""
	return &resolved
}

func (client *Client) responseLimit() int64 {
	if client.MaxResponseBytes <= 0 {
		return defaultControlResponseLimit
	}
	return client.MaxResponseBytes
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func decodeLimitedJSON(reader io.Reader, limit int64, destination any) error {
	if limit <= 0 {
		limit = defaultControlResponseLimit
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return fmt.Errorf("%w: read response: %v", ErrControlInvalidResponse, err)
	}
	if int64(len(data)) > limit {
		return fmt.Errorf("%w: response exceeds %d bytes", ErrControlInvalidResponse, limit)
	}
	return decodeStrictJSONBytes(data, destination)
}

func decodeStrictJSONBytes(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%w: %v", ErrControlInvalidResponse, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: multiple JSON values", ErrControlInvalidResponse)
		}
		return fmt.Errorf("%w: trailing JSON: %v", ErrControlInvalidResponse, err)
	}
	return nil
}

type enrollmentWireResponse struct {
	Node         nodeWireResponse       `json:"node"`
	Membership   membershipWireResponse `json:"membership"`
	Network      networkWireResponse    `json:"network"`
	Session      sessionResponse        `json:"session"`
	SessionToken string                 `json:"session_token"`
}

type nodeWireResponse struct {
	ID            string    `json:"id"`
	DisplayName   string    `json:"display_name"`
	PublicKey     string    `json:"public_key"`
	Platform      string    `json:"platform"`
	ClientVersion string    `json:"client_version"`
	LastSeen      time.Time `json:"last_seen"`
}

type membershipWireResponse struct {
	NetworkID   string `json:"network_id"`
	NodeID      string `json:"node_id"`
	VirtualIPv4 net.IP `json:"virtual_ipv4"`
	Role        string `json:"role"`
	Status      string `json:"status"`
}

type networkWireResponse struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Pool          string    `json:"pool"`
	IPv4Pool      string    `json:"ipv4_pool"`
	OwnerID       string    `json:"owner_id"`
	ConfigVersion int64     `json:"config_version"`
	CreatedAt     time.Time `json:"created_at"`
}

type heartbeatWireRequest struct {
	Endpoints     []heartbeatWireEndpoint `json:"endpoints"`
	ClientVersion *string                 `json:"client_version,omitempty"`
}

type heartbeatWireEndpoint struct {
	Address    string `json:"address"`
	Port       uint16 `json:"port"`
	Family     string `json:"family"`
	Interface  string `json:"interface"`
	Priority   int    `json:"priority"`
	ObservedAt string `json:"observed_at"`
}

type snapshotWireResponse struct {
	NetworkID        string             `json:"network_id"`
	Generation       int64              `json:"generation"`
	ConfigVersion    int64              `json:"config_version"`
	LocalNodeID      string             `json:"local_node_id"`
	LocalVirtualIPv4 net.IP             `json:"local_virtual_ipv4"`
	Peers            []peerWireResponse `json:"peers"`
	RelayAssignment  *relayWireResponse `json:"relay_assignment"`
	GeneratedAt      time.Time          `json:"generated_at"`
}

type peerWireResponse struct {
	NodeID      string                 `json:"node_id"`
	DisplayName string                 `json:"display_name"`
	PublicKey   string                 `json:"public_key"`
	VirtualIPv4 net.IP                 `json:"virtual_ipv4"`
	Node        nodeWireResponse       `json:"node"`
	Membership  membershipWireResponse `json:"membership"`
	Endpoints   []endpointWireResponse `json:"endpoints"`
}

type endpointWireResponse struct {
	NodeID     string    `json:"node_id"`
	Address    net.IP    `json:"address"`
	Port       uint16    `json:"port"`
	Family     string    `json:"family"`
	Interface  string    `json:"interface"`
	Priority   int       `json:"priority"`
	ObservedAt time.Time `json:"observed_at"`
}

type relayWireResponse struct {
	ID          string     `json:"id"`
	NetworkID   string     `json:"network_id"`
	NodeID      string     `json:"node_id"`
	RelayNodeID string     `json:"relay_node_id"`
	Address     net.IP     `json:"address"`
	Port        uint16     `json:"port"`
	Family      string     `json:"family"`
	Status      string     `json:"status"`
	AssignedAt  time.Time  `json:"assigned_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
}

func nodeFromWire(value nodeWireResponse) Node {
	return Node{ID: value.ID, DisplayName: value.DisplayName, PublicKey: value.PublicKey, Platform: value.Platform, ClientVersion: value.ClientVersion, LastSeen: value.LastSeen}
}

func membershipFromWire(value membershipWireResponse) Membership {
	return Membership{NetworkID: value.NetworkID, NodeID: value.NodeID, VirtualIPv4: cloneIP(value.VirtualIPv4), Role: value.Role, Status: value.Status}
}

func networkFromWire(value networkWireResponse) Network {
	pool := value.IPv4Pool
	if pool == "" {
		pool = value.Pool
	}
	return Network{ID: value.ID, Name: value.Name, IPv4Pool: pool, OwnerID: value.OwnerID, ConfigVersion: value.ConfigVersion, CreatedAt: value.CreatedAt}
}

func snapshotFromWire(value snapshotWireResponse) (NetworkSnapshot, error) {
	if strings.TrimSpace(value.NetworkID) == "" || value.Generation <= 0 || strings.TrimSpace(value.LocalNodeID) == "" || value.LocalVirtualIPv4.To4() == nil {
		return NetworkSnapshot{}, fmt.Errorf("%w: snapshot identity or generation is invalid", ErrControlInvalidResponse)
	}
	snapshot := NetworkSnapshot{NetworkID: value.NetworkID, Generation: value.Generation, ConfigVersion: value.ConfigVersion, LocalNodeID: value.LocalNodeID, LocalVirtualIPv4: cloneIP(value.LocalVirtualIPv4), GeneratedAt: value.GeneratedAt}
	snapshot.Peers = make([]Peer, len(value.Peers))
	for index, peer := range value.Peers {
		converted := Peer{NodeID: peer.NodeID, DisplayName: peer.DisplayName, PublicKey: peer.PublicKey, VirtualIPv4: cloneIP(peer.VirtualIPv4), Node: nodeFromWire(peer.Node), Membership: membershipFromWire(peer.Membership), Endpoints: make([]EndpointCandidate, len(peer.Endpoints))}
		for endpointIndex, endpoint := range peer.Endpoints {
			converted.Endpoints[endpointIndex] = EndpointCandidate{NodeID: endpoint.NodeID, Address: cloneIP(endpoint.Address), Port: endpoint.Port, Family: endpoint.Family, Interface: endpoint.Interface, Priority: endpoint.Priority, ObservedAt: endpoint.ObservedAt}
		}
		snapshot.Peers[index] = converted
	}
	if value.RelayAssignment != nil {
		relay := relayFromWire(*value.RelayAssignment)
		snapshot.RelayAssignment = &relay
	}
	return snapshot, nil
}

func relayFromWire(value relayWireResponse) RelayAssignment {
	return RelayAssignment{ID: value.ID, NetworkID: value.NetworkID, NodeID: value.NodeID, RelayNodeID: value.RelayNodeID, Address: cloneIP(value.Address), Port: value.Port, Family: value.Family, Status: value.Status, AssignedAt: value.AssignedAt, ExpiresAt: value.ExpiresAt}
}

func cloneIP(value net.IP) net.IP {
	if value == nil {
		return nil
	}
	return append(net.IP(nil), value...)
}
