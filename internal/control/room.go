package control

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Eser-s-Organization/ipv6mesh/internal/address"
	"github.com/Eser-s-Organization/ipv6mesh/internal/auth"
)

const (
	defaultRoomJoinPerIP        = 10
	defaultRoomJoinGlobal       = 100
	roomJoinWindow              = time.Minute
	roomLimiterEntryTTL         = 2 * time.Minute
	roomJoinBodyLimit     int64 = 64 << 10
)

var (
	ErrRoomModeDisabled  = errors.New("room mode disabled")
	ErrRoomNotReady      = errors.New("room not ready")
	ErrRoomAlreadyExists = errors.New("room already exists")
)

type roomCoordinator struct {
	mu        sync.RWMutex
	networkID string
}

type roomLimitEntry struct {
	window   time.Time
	count    int
	lastSeen time.Time
}

type roomJoinLimiter struct {
	mu           sync.Mutex
	now          func() time.Time
	perIP        int
	global       int
	globalWindow time.Time
	globalCount  int
	sources      map[string]roomLimitEntry
}

func newRoomJoinLimiter(perIP, global int, now func() time.Time) *roomJoinLimiter {
	if perIP <= 0 {
		perIP = defaultRoomJoinPerIP
	}
	if global <= 0 {
		global = defaultRoomJoinGlobal
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &roomJoinLimiter{now: now, perIP: perIP, global: global, sources: make(map[string]roomLimitEntry)}
}

func (limiter *roomJoinLimiter) allow(remoteAddr string) bool {
	if limiter == nil {
		return false
	}
	source, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil || strings.TrimSpace(source) == "" {
		return false
	}
	now := limiter.now().UTC()
	window := now.Truncate(roomJoinWindow)
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	for key, entry := range limiter.sources {
		if now.Sub(entry.lastSeen) > roomLimiterEntryTTL {
			delete(limiter.sources, key)
		}
	}
	if limiter.globalWindow != window {
		limiter.globalWindow = window
		limiter.globalCount = 0
	}
	entry := limiter.sources[source]
	if entry.window != window {
		entry = roomLimitEntry{window: window}
	}
	if limiter.globalCount >= limiter.global || entry.count >= limiter.perIP {
		return false
	}
	limiter.globalCount++
	entry.count++
	entry.lastSeen = now
	limiter.sources[source] = entry
	return true
}

func (room *roomCoordinator) active() (string, bool) {
	if room == nil {
		return "", false
	}
	room.mu.RLock()
	defer room.mu.RUnlock()
	return room.networkID, room.networkID != ""
}

func (room *roomCoordinator) create(ctx context.Context, repository Repository, network Network) error {
	if room == nil {
		return ErrRoomModeDisabled
	}
	room.mu.Lock()
	defer room.mu.Unlock()
	if room.networkID != "" {
		return ErrRoomAlreadyExists
	}
	if err := repository.CreateNetwork(ctx, network); err != nil {
		return err
	}
	room.networkID = network.ID
	return nil
}

func writeErrorCode(writer http.ResponseWriter, status int, code string) {
	writeJSON(writer, status, map[string]string{"error": code})
}

func (handler *Handler) createRoom(writer http.ResponseWriter, request *http.Request) {
	if handler.room == nil {
		writeErrorCode(writer, http.StatusNotFound, "room_mode_disabled")
		return
	}
	principal, err := handler.requireAdmin(request)
	if err != nil {
		writeErrorCode(writer, statusForError(err), "unauthorized")
		return
	}
	var body struct {
		Name string `json:"name"`
		Pool string `json:"ipv4_pool"`
	}
	if err := decodeJSON(writer, request, handler.maxBodyBytes, &body); err != nil {
		writeErrorCode(writer, http.StatusUnprocessableEntity, "invalid_room")
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeErrorCode(writer, http.StatusUnprocessableEntity, "invalid_room")
		return
	}
	if _, err := address.NewPool(body.Pool); err != nil {
		writeErrorCode(writer, http.StatusUnprocessableEntity, "invalid_room")
		return
	}
	network := Network{
		ID:            handler.newID(),
		Name:          strings.TrimSpace(body.Name),
		IPv4Pool:      body.Pool,
		OwnerID:       principal.session.Subject,
		ConfigVersion: 1,
		CreatedAt:     handler.clock().UTC(),
	}
	if err := handler.room.create(request.Context(), handler.repository, network); err != nil {
		switch {
		case errors.Is(err, ErrRoomAlreadyExists):
			writeErrorCode(writer, http.StatusConflict, "room_already_exists")
		default:
			writeAPIError(writer, statusForError(err), err)
		}
		return
	}
	writeJSON(writer, http.StatusCreated, makeNetworkResponse(network))
}

func (handler *Handler) joinRoom(writer http.ResponseWriter, request *http.Request) {
	if handler.room == nil {
		writeErrorCode(writer, http.StatusNotFound, "room_mode_disabled")
		return
	}
	if _, ok := handler.room.active(); !ok {
		writeErrorCode(writer, http.StatusNotFound, "room_not_ready")
		return
	}
	if !handler.roomLimiter.allow(request.RemoteAddr) {
		writeErrorCode(writer, http.StatusTooManyRequests, "join_rate_limited")
		return
	}
	var body struct {
		PublicKey     string `json:"public_key"`
		DisplayName   string `json:"display_name"`
		Platform      string `json:"platform"`
		ClientVersion string `json:"client_version"`
	}
	if err := decodeJSON(writer, request, roomJoinBodyLimit, &body); err != nil {
		if errors.Is(err, ErrRequestTooLarge) {
			writeErrorCode(writer, http.StatusRequestEntityTooLarge, "request_too_large")
		} else {
			writeErrorCode(writer, http.StatusUnprocessableEntity, "invalid_node")
		}
		return
	}
	roomNetworkID, ok := handler.room.active()
	if !ok {
		writeErrorCode(writer, http.StatusNotFound, "room_not_ready")
		return
	}
	_, inviteToken, err := handler.newInternalRoomInvite(request.Context(), roomNetworkID)
	if err != nil {
		writeAPIError(writer, statusForError(err), err)
		return
	}
	result, enrollErr := handler.enrollControl(request.Context(), enrollmentRequest{
		InviteToken:   inviteToken,
		DisplayName:   body.DisplayName,
		PublicKey:     body.PublicKey,
		Platform:      body.Platform,
		ClientVersion: body.ClientVersion,
	})
	if enrollErr != nil && result.SessionToken == "" {
		if revokeErr := handler.repository.RevokeInvite(request.Context(), strings.SplitN(inviteToken, ".", 2)[0], handler.clock().UTC()); revokeErr != nil {
			enrollErr = errors.Join(enrollErr, revokeErr)
		}
	}
	if enrollErr != nil {
		switch {
		case errors.Is(enrollErr, address.ErrPoolExhausted):
			writeErrorCode(writer, http.StatusConflict, "room_full")
		case errors.Is(enrollErr, ErrConflict):
			writeErrorCode(writer, http.StatusConflict, "node_already_joined")
		case errors.Is(enrollErr, ErrRequestTooLarge):
			writeErrorCode(writer, http.StatusRequestEntityTooLarge, "request_too_large")
		case errors.Is(enrollErr, ErrInvalidRequest), errors.Is(enrollErr, ErrValidation):
			writeErrorCode(writer, http.StatusUnprocessableEntity, "invalid_node")
		default:
			handler.writeEnrollmentResult(writer, result, enrollErr)
		}
		return
	}
	handler.writeEnrollmentResult(writer, result, nil)
}

func (handler *Handler) newInternalRoomInvite(ctx context.Context, networkID string) (string, string, error) {
	inviteID := handler.newID()
	secret := make([]byte, 32)
	if _, err := io.ReadFull(handler.tokenRandom, secret); err != nil {
		return "", "", err
	}
	token := inviteID + "." + base64.RawURLEncoding.EncodeToString(secret)
	now := handler.clock().UTC()
	invite := Invite{ID: inviteID, NetworkID: networkID, TokenHash: auth.HashToken(token), CreatedAt: now, ExpiresAt: now.Add(handler.inviteTTL)}
	if err := handler.repository.CreateInvite(ctx, invite); err != nil {
		return "", "", err
	}
	return inviteID, token, nil
}
