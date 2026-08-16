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
