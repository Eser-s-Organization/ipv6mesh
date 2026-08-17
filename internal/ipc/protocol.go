// Package ipc defines the versioned local service protocol. It is deliberately
// independent of Windows so it can be exhaustively tested on every platform.
package ipc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	MaxMessageSize  = 1 << 20
	DefaultPipeName = `\\.\pipe\ipv6mesh`
)

var (
	ErrInvalidRequest  = errors.New("invalid IPC request")
	ErrInvalidResponse = errors.New("invalid IPC response")
	ErrMessageTooLarge = errors.New("IPC message is too large")
)

type Command string

const (
	CommandStatus      Command = "status"
	CommandJoin        Command = "join"
	CommandJoinRoom    Command = "join_room"
	CommandLeave       Command = "leave"
	CommandConnect     Command = "connect"
	CommandDisconnect  Command = "disconnect"
	CommandRoomMembers Command = "room_members"
)

type RoomMemberState string

const RoomMemberOnline RoomMemberState = "online"

type RoomMember struct {
	DisplayName string          `json:"display_name"`
	VirtualIPv4 string          `json:"virtual_ipv4"`
	IsLocal     bool            `json:"is_local"`
	State       RoomMemberState `json:"state"`
}

type PathState string

const (
	PathStateDisconnected PathState = "disconnected"
	PathStateDirect       PathState = "direct"
	PathStateRelay        PathState = "relay"
)

const (
	CodeInvalidRequest = "invalid_request"
	CodeUnauthorized   = "unauthorized"
	CodeAlreadyJoined  = "already_joined"
	CodeNotJoined      = "not_joined"
	CodeWrongNetwork   = "wrong_network"
	CodeNotStarted     = "not_started"
	CodeControlFailed  = "control_failed"
	CodeAdapterFailed  = "adapter_failed"
	CodeInternal       = "internal_error"
)

type Request struct {
	Type        Command
	Invite      string
	DisplayName string
	ControlURL  string
	NetworkID   string
}

type Status struct {
	NetworkID        string    `json:"network_id,omitempty"`
	VirtualIPv4      string    `json:"virtual_ipv4,omitempty"`
	PathState        PathState `json:"path_state"`
	LastHandshake    time.Time `json:"last_handshake,omitempty"`
	LastError        string    `json:"last_error,omitempty"`
	ConfigGeneration int64     `json:"config_generation"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

type Response struct {
	OK bool `json:"ok"`
	Status
	Members []RoomMember `json:"members,omitempty"`
	Error   *Error       `json:"error,omitempty"`
}

func SuccessResponse(status Status) Response {
	if status.PathState == "" {
		status.PathState = PathStateDisconnected
	}
	return Response{OK: true, Status: status}
}

func ErrorResponse(code string) Response {
	if code == "" {
		code = CodeInternal
	}
	return Response{Error: &Error{Code: code}}
}

func SuccessMembersResponse(networkID string, members []RoomMember) Response {
	copyMembers := append([]RoomMember(nil), members...)
	return Response{
		OK: true,
		Status: Status{
			NetworkID: networkID,
			PathState: PathStateDisconnected,
		},
		Members: copyMembers,
	}
}

func MarshalRequest(request Request) ([]byte, error) {
	if err := validateRequest(request); err != nil {
		return nil, err
	}
	fields := map[string]any{"type": request.Type}
	switch request.Type {
	case CommandJoin:
		fields["invite"] = request.Invite
		fields["display_name"] = request.DisplayName
	case CommandJoinRoom:
		fields["control_url"] = request.ControlURL
		fields["display_name"] = request.DisplayName
	case CommandLeave, CommandConnect, CommandDisconnect:
		fields["network_id"] = request.NetworkID
	}
	return json.Marshal(fields)
}

func DecodeRequest(data []byte) (Request, error) {
	fields, err := decodeObject(data, true)
	if err != nil {
		return Request{}, errors.Join(ErrInvalidRequest, err)
	}
	var request Request
	var command string
	if err := decodeStringField(fields, "type", &command); err != nil {
		return Request{}, errors.Join(ErrInvalidRequest, err)
	}
	request.Type = Command(command)
	allowed := map[string]bool{"type": true}
	switch request.Type {
	case CommandStatus, CommandRoomMembers:
	case CommandJoin:
		allowed["invite"], allowed["display_name"] = true, true
		if err := decodeStringField(fields, "invite", &request.Invite); err != nil {
			return Request{}, errors.Join(ErrInvalidRequest, err)
		}
		if err := decodeStringField(fields, "display_name", &request.DisplayName); err != nil {
			return Request{}, errors.Join(ErrInvalidRequest, err)
		}
	case CommandJoinRoom:
		allowed["control_url"], allowed["display_name"] = true, true
		if err := decodeStringField(fields, "control_url", &request.ControlURL); err != nil {
			return Request{}, errors.Join(ErrInvalidRequest, err)
		}
		if err := decodeStringField(fields, "display_name", &request.DisplayName); err != nil {
			return Request{}, errors.Join(ErrInvalidRequest, err)
		}
	case CommandLeave, CommandConnect, CommandDisconnect:
		allowed["network_id"] = true
		if err := decodeStringField(fields, "network_id", &request.NetworkID); err != nil {
			return Request{}, errors.Join(ErrInvalidRequest, err)
		}
	default:
		return Request{}, errors.Join(ErrInvalidRequest, fmt.Errorf("unsupported command %q", request.Type))
	}
	for name := range fields {
		if !allowed[name] {
			return Request{}, errors.Join(ErrInvalidRequest, fmt.Errorf("unknown field %q", name))
		}
	}
	if err := validateRequest(request); err != nil {
		return Request{}, errors.Join(ErrInvalidRequest, err)
	}
	return request, nil
}

func MarshalResponse(response Response) ([]byte, error) {
	if response.OK && response.Error != nil {
		return nil, ErrInvalidResponse
	}
	if !response.OK && response.Error == nil {
		return nil, ErrInvalidResponse
	}
	if !response.OK && response.Members != nil {
		return nil, ErrInvalidResponse
	}
	status := response.Status
	if status.PathState == "" {
		status.PathState = PathStateDisconnected
	}
	fields := map[string]any{"ok": response.OK, "path_state": status.PathState, "config_generation": status.ConfigGeneration}
	if status.NetworkID != "" {
		fields["network_id"] = status.NetworkID
	}
	if status.VirtualIPv4 != "" {
		fields["virtual_ipv4"] = status.VirtualIPv4
	}
	if !status.LastHandshake.IsZero() {
		fields["last_handshake"] = status.LastHandshake.UTC()
	}
	if status.LastError != "" {
		fields["last_error"] = status.LastError
	}
	if response.Members != nil {
		if strings.TrimSpace(status.NetworkID) == "" {
			return nil, ErrInvalidResponse
		}
		for _, member := range response.Members {
			if err := validateRoomMember(member); err != nil {
				return nil, errors.Join(ErrInvalidResponse, err)
			}
		}
		fields["members"] = response.Members
	}
	if response.Error != nil {
		fields["error"] = response.Error
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxMessageSize {
		return nil, ErrMessageTooLarge
	}
	return encoded, nil
}

func DecodeResponse(data []byte) (Response, error) {
	fields, err := decodeObject(data, true)
	if err != nil {
		return Response{}, errors.Join(ErrInvalidResponse, err)
	}
	var response Response
	if err := decodeBoolField(fields, "ok", &response.OK); err != nil {
		return Response{}, errors.Join(ErrInvalidResponse, err)
	}
	allowed := map[string]bool{"ok": true, "network_id": true, "virtual_ipv4": true, "path_state": true, "last_handshake": true, "last_error": true, "config_generation": true, "members": true, "error": true}
	for name := range fields {
		if !allowed[name] {
			return Response{}, errors.Join(ErrInvalidResponse, fmt.Errorf("unknown field %q", name))
		}
	}
	if raw, ok := fields["network_id"]; ok {
		if err := decodeString(raw, &response.NetworkID); err != nil {
			return Response{}, errors.Join(ErrInvalidResponse, err)
		}
	}
	if raw, ok := fields["virtual_ipv4"]; ok {
		if err := decodeString(raw, &response.VirtualIPv4); err != nil {
			return Response{}, errors.Join(ErrInvalidResponse, err)
		}
	}
	if raw, ok := fields["path_state"]; ok {
		var pathState string
		if err := decodeString(raw, &pathState); err != nil {
			return Response{}, errors.Join(ErrInvalidResponse, err)
		}
		response.PathState = PathState(pathState)
	}
	if raw, ok := fields["last_handshake"]; ok {
		if err := decodeTime(raw, &response.LastHandshake); err != nil {
			return Response{}, errors.Join(ErrInvalidResponse, err)
		}
	}
	if raw, ok := fields["last_error"]; ok {
		if err := decodeString(raw, &response.LastError); err != nil {
			return Response{}, errors.Join(ErrInvalidResponse, err)
		}
	}
	if raw, ok := fields["config_generation"]; ok {
		if err := decodeInt(raw, &response.ConfigGeneration); err != nil {
			return Response{}, errors.Join(ErrInvalidResponse, err)
		}
	}
	if raw, ok := fields["members"]; ok {
		if !response.OK {
			return Response{}, errors.Join(ErrInvalidResponse, errors.New("members are only valid on success responses"))
		}
		members, err := decodeRoomMembers(raw)
		if err != nil {
			return Response{}, errors.Join(ErrInvalidResponse, err)
		}
		response.Members = members
	}
	if raw, ok := fields["error"]; ok {
		var value map[string]json.RawMessage
		if err := json.Unmarshal(raw, &value); err != nil || value == nil {
			return Response{}, errors.Join(ErrInvalidResponse, errors.New("invalid error object"))
		}
		var code string
		if err := decodeStringField(value, "code", &code); err != nil {
			return Response{}, errors.Join(ErrInvalidResponse, err)
		}
		message := ""
		if messageRaw, exists := value["message"]; exists {
			if err := decodeString(messageRaw, &message); err != nil {
				return Response{}, errors.Join(ErrInvalidResponse, err)
			}
		}
		for name := range value {
			if name != "code" && name != "message" {
				return Response{}, errors.Join(ErrInvalidResponse, fmt.Errorf("unknown error field %q", name))
			}
		}
		response.Error = &Error{Code: code, Message: message}
	}
	if response.OK == (response.Error != nil) {
		return Response{}, ErrInvalidResponse
	}
	if response.PathState == "" {
		response.PathState = PathStateDisconnected
	}
	return response, nil
}

func validateRequest(request Request) error {
	switch request.Type {
	case CommandStatus, CommandRoomMembers:
		if request.Invite != "" || request.DisplayName != "" || request.ControlURL != "" || request.NetworkID != "" {
			return errors.New("status has no arguments")
		}
	case CommandJoin:
		if strings.TrimSpace(request.Invite) == "" || strings.TrimSpace(request.DisplayName) == "" {
			return errors.New("join requires invite and display_name")
		}
	case CommandJoinRoom:
		if strings.TrimSpace(request.ControlURL) == "" || strings.TrimSpace(request.DisplayName) == "" {
			return errors.New("join_room requires control_url and display_name")
		}
		if request.Invite != "" || request.NetworkID != "" {
			return errors.New("join_room has no invite or network_id")
		}
	case CommandLeave, CommandConnect, CommandDisconnect:
		if strings.TrimSpace(request.NetworkID) == "" {
			return errors.New("command requires network_id")
		}
	default:
		return errors.New("unsupported command")
	}
	return nil
}

func validateRoomMember(member RoomMember) error {
	if strings.TrimSpace(member.DisplayName) == "" {
		return errors.New("room member display_name is required")
	}
	if net.ParseIP(member.VirtualIPv4).To4() == nil {
		return errors.New("room member virtual_ipv4 must be IPv4")
	}
	if member.State != RoomMemberOnline {
		return errors.New("room member state must be online")
	}
	return nil
}

func decodeRoomMember(raw json.RawMessage) (RoomMember, error) {
	fields, err := decodeObject(raw, true)
	if err != nil {
		return RoomMember{}, err
	}
	allowed := map[string]bool{"display_name": true, "virtual_ipv4": true, "is_local": true, "state": true}
	for name := range fields {
		if !allowed[name] {
			return RoomMember{}, fmt.Errorf("unknown room member field %q", name)
		}
	}
	var member RoomMember
	if err := decodeStringField(fields, "display_name", &member.DisplayName); err != nil {
		return RoomMember{}, err
	}
	if err := decodeStringField(fields, "virtual_ipv4", &member.VirtualIPv4); err != nil {
		return RoomMember{}, err
	}
	if raw, ok := fields["is_local"]; !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return RoomMember{}, errors.New("missing or null room member is_local")
	} else if err := json.Unmarshal(raw, &member.IsLocal); err != nil {
		return RoomMember{}, err
	}
	var state string
	if err := decodeStringField(fields, "state", &state); err != nil {
		return RoomMember{}, err
	}
	member.State = RoomMemberState(state)
	if err := validateRoomMember(member); err != nil {
		return RoomMember{}, err
	}
	return member, nil
}

func decodeRoomMembers(raw json.RawMessage) ([]RoomMember, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errors.New("members cannot be null")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := first.(json.Delim)
	if !ok || delim != '[' {
		return nil, errors.New("members array required")
	}
	members := make([]RoomMember, 0)
	for decoder.More() {
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		member, err := decodeRoomMember(value)
		if err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("trailing JSON value")
		}
		return nil, err
	}
	return members, nil
}

func decodeObject(data []byte, rejectTrailing bool) (map[string]json.RawMessage, error) {
	if len(data) > MaxMessageSize {
		return nil, ErrMessageTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	first, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := first.(json.Delim)
	if !ok || delim != '{' {
		return nil, errors.New("JSON object required")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := key.(string)
		if !ok || name == "" {
			return nil, errors.New("invalid JSON object key")
		}
		if _, exists := fields[name]; exists {
			return nil, fmt.Errorf("duplicate field %q", name)
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		fields[name] = raw
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if rejectTrailing {
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			if err == nil {
				return nil, errors.New("trailing JSON value")
			}
			return nil, err
		}
	}
	return fields, nil
}

func decodeStringField(fields map[string]json.RawMessage, name string, target *string) error {
	raw, ok := fields[name]
	if !ok {
		return fmt.Errorf("missing field %q", name)
	}
	return decodeString(raw, target)
}

func decodeString(raw json.RawMessage, target *string) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("string field cannot be null")
	}
	return json.Unmarshal(raw, target)
}

func decodeBoolField(fields map[string]json.RawMessage, name string, target *bool) error {
	raw, ok := fields[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("missing or null field %q", name)
	}
	return json.Unmarshal(raw, target)
}

func decodeTime(raw json.RawMessage, target *time.Time) error {
	var value string
	if err := decodeString(raw, &value); err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return err
	}
	*target = parsed
	return nil
}

func decodeInt(raw json.RawMessage, target *int64) error {
	value, err := strconv.ParseInt(string(bytes.TrimSpace(raw)), 10, 64)
	if err != nil {
		return err
	}
	*target = value
	return nil
}
