package control

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/Eser-s-Organization/ipv6mesh/internal/address"
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

// joinRoom is completed by the room-enrollment task. Keeping the route
// explicit now ensures room mode cannot accidentally fall through to a legacy
// endpoint while the feature is being assembled.
func (handler *Handler) joinRoom(writer http.ResponseWriter, request *http.Request) {
	if handler.room == nil {
		writeErrorCode(writer, http.StatusNotFound, "room_mode_disabled")
		return
	}
	if _, ok := handler.room.active(); !ok {
		writeErrorCode(writer, http.StatusNotFound, "room_not_ready")
		return
	}
	writeErrorCode(writer, http.StatusNotImplemented, "room_join_not_implemented")
}
