package ipc

import (
	"errors"
	"strings"
	"testing"
)

func TestDecodeRequestAcceptsSupportedCommands(t *testing.T) {
	tests := []struct {
		name  string
		json  string
		type_ Command
	}{
		{name: "status", json: `{"type":"status"}`, type_: CommandStatus},
		{name: "room members", json: `{"type":"room_members"}`, type_: CommandRoomMembers},
		{name: "join", json: `{"type":"join","invite":"invite-value","display_name":"device-a"}`, type_: CommandJoin},
		{name: "leave", json: `{"type":"leave","network_id":"network-a"}`, type_: CommandLeave},
		{name: "connect", json: `{"type":"connect","network_id":"network-a"}`, type_: CommandConnect},
		{name: "disconnect", json: `{"type":"disconnect","network_id":"network-a"}`, type_: CommandDisconnect},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := DecodeRequest([]byte(test.json))
			if err != nil {
				t.Fatalf("DecodeRequest: %v", err)
			}
			if request.Type != test.type_ {
				t.Fatalf("request type = %q, want %q", request.Type, test.type_)
			}
		})
	}
}

func TestCommandTimeoutClass(t *testing.T) {
	for _, command := range []Command{CommandStatus, CommandConnect, CommandDisconnect} {
		if got := commandTimeoutClass(command); got != localCommandTimeout {
			t.Errorf("%s = %v", command, got)
		}
	}
	for _, command := range []Command{CommandJoin, CommandJoinRoom, CommandLeave, CommandRoomMembers} {
		if got := commandTimeoutClass(command); got != networkCommandTimeout {
			t.Errorf("%s = %v", command, got)
		}
	}
}

func TestRoomMembersRequestRoundTripAndRejectsArguments(t *testing.T) {
	request := Request{Type: CommandRoomMembers}
	encoded, err := MarshalRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"type":"room_members"}` {
		t.Fatalf("encoded = %s", encoded)
	}
	decoded, err := DecodeRequest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != request {
		t.Fatalf("decoded = %#v, want %#v", decoded, request)
	}
	for _, value := range []string{
		`{"type":"room_members","network_id":"room-1"}`,
		`{"type":"room_members","session_token":"secret"}`,
		`{"type":"room_members","display_name":"MEMBER-PC"}`,
	} {
		if _, err := DecodeRequest([]byte(value)); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("DecodeRequest(%s) error = %v", value, err)
		}
	}
}

func TestRoomMembersResponseRoundTripIsSanitized(t *testing.T) {
	response := SuccessMembersResponse("room-1", []RoomMember{
		{DisplayName: "HOST-PC", VirtualIPv4: "10.42.0.2", IsLocal: true, State: RoomMemberOnline},
		{DisplayName: "MEMBER-PC", VirtualIPv4: "10.42.0.3", State: RoomMemberOnline},
	})
	encoded, err := MarshalResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"session_token", "public_key", "private_key", "node_id", "endpoint", "last_seen"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("response contains %q: %s", forbidden, encoded)
		}
	}
	decoded, err := DecodeResponse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.OK || decoded.NetworkID != "room-1" || len(decoded.Members) != 2 {
		t.Fatalf("decoded = %#v", decoded)
	}
	if decoded.Members[0].DisplayName != "HOST-PC" || !decoded.Members[0].IsLocal || decoded.Members[0].State != RoomMemberOnline {
		t.Fatalf("local member = %#v", decoded.Members[0])
	}
}

func TestRoomMembersResponseRejectsUnknownNullAndInvalidFields(t *testing.T) {
	values := []string{
		`{"ok":true,"path_state":"disconnected","config_generation":0,"network_id":"room-1","members":null}`,
		`{"ok":true,"path_state":"disconnected","config_generation":0,"network_id":"room-1","members":[{"display_name":"A","virtual_ipv4":"10.42.0.2","is_local":true,"state":"online","token":"secret"}]}`,
		`{"ok":true,"path_state":"disconnected","config_generation":0,"network_id":"room-1","members":[{"display_name":"","virtual_ipv4":"10.42.0.2","is_local":true,"state":"online"}]}`,
		`{"ok":true,"path_state":"disconnected","config_generation":0,"network_id":"room-1","members":[{"display_name":"A","virtual_ipv4":"2001:db8::1","is_local":true,"state":"online"}]}`,
		`{"ok":true,"path_state":"disconnected","config_generation":0,"network_id":"room-1","members":[{"display_name":"A","virtual_ipv4":"10.42.0.2","is_local":true,"state":"offline"}]}`,
	}
	for _, value := range values {
		if _, err := DecodeResponse([]byte(value)); !errors.Is(err, ErrInvalidResponse) {
			t.Fatalf("DecodeResponse(%s) error = %v", value, err)
		}
	}
}

func TestJoinRoomRequestRoundTrip(t *testing.T) {
	request := Request{
		Type:        CommandJoinRoom,
		ControlURL:  "http://[2001:db8::1]:8080",
		DisplayName: "MEMBER-PC",
	}
	encoded, err := MarshalRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRequest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != request {
		t.Fatalf("decoded = %#v, want %#v", decoded, request)
	}
}

func TestJoinRoomRejectsMissingAndUnknownFields(t *testing.T) {
	for _, value := range []string{
		`{"type":"join_room","display_name":"MEMBER-PC"}`,
		`{"type":"join_room","control_url":"http://[2001:db8::1]:8080"}`,
		`{"type":"join_room","control_url":"http://[2001:db8::1]:8080","display_name":"MEMBER-PC","invite":"secret"}`,
	} {
		if _, err := DecodeRequest([]byte(value)); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("DecodeRequest(%s) error = %v", value, err)
		}
	}
}

func TestDecodeRequestRejectsUnknownDuplicateAndMissingFields(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{name: "unknown", json: `{"type":"status","private_key":"do-not-accept"}`},
		{name: "duplicate", json: `{"type":"status","type":"status"}`},
		{name: "missing join invite", json: `{"type":"join","display_name":"device-a"}`},
		{name: "unexpected join field", json: `{"type":"join","invite":"token","display_name":"device-a","network_id":"network-a"}`},
		{name: "trailing value", json: `{"type":"status"}{"type":"status"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeRequest([]byte(test.json)); err == nil {
				t.Fatal("DecodeRequest accepted invalid request")
			}
		})
	}
}

func TestDecodeRequestEnforcesMessageLimit(t *testing.T) {
	payload := `{"type":"status","padding":"` + strings.Repeat("x", MaxMessageSize) + `"}`
	if _, err := DecodeRequest([]byte(payload)); err == nil {
		t.Fatal("DecodeRequest accepted an oversized request")
	}
}

func TestResponseNeverContainsRawTokenOrPrivateKeyFields(t *testing.T) {
	response := SuccessResponse(Status{
		NetworkID:        "network-a",
		VirtualIPv4:      "100.64.0.2",
		PathState:        PathStateDisconnected,
		LastError:        "none",
		ConfigGeneration: 3,
	})
	encoded, err := MarshalResponse(response)
	if err != nil {
		t.Fatalf("MarshalResponse: %v", err)
	}
	if strings.Contains(string(encoded), "invite-value") || strings.Contains(string(encoded), "private_key") || strings.Contains(string(encoded), "session_token") {
		t.Fatalf("response contains sensitive fields: %s", encoded)
	}
	decoded, err := DecodeResponse(encoded)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if decoded.NetworkID != response.NetworkID || decoded.ConfigGeneration != response.ConfigGeneration {
		t.Fatalf("decoded response does not preserve status: %#v", decoded)
	}
}
