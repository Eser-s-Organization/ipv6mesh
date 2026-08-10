package control

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// ErrValidation is the stable sentinel for invalid control-plane models.
var ErrValidation = errors.New("validation failed")

// ValidationError identifies the first invalid field while retaining the
// stable ErrValidation identity for callers.
type ValidationError struct {
	Model  string
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	if e.Model == "" {
		return fmt.Sprintf("%s: %s", e.Field, e.Reason)
	}
	return fmt.Sprintf("%s.%s: %s", e.Model, e.Field, e.Reason)
}

func (e *ValidationError) Unwrap() error { return ErrValidation }

func invalid(model, field, reason string) error {
	return &ValidationError{Model: model, Field: field, Reason: reason}
}

func required(model, field, value string) error {
	if strings.TrimSpace(value) == "" {
		return invalid(model, field, "is required")
	}
	return nil
}

// ValidateNetwork validates the network identity and its IPv4-only pool.
func ValidateNetwork(network Network) error {
	if err := required("network", "id", network.ID); err != nil {
		return err
	}
	if err := required("network", "name", network.Name); err != nil {
		return err
	}
	if err := required("network", "owner_id", network.OwnerID); err != nil {
		return err
	}
	poolIP, pool, err := net.ParseCIDR(network.IPv4Pool)
	if err != nil || poolIP.To4() == nil {
		return invalid("network", "ipv4_pool", "must be an IPv4 network")
	}
	if !poolIP.Equal(pool.IP) {
		return invalid("network", "ipv4_pool", "must not contain host bits")
	}
	if _, bits := pool.Mask.Size(); bits != 32 {
		return invalid("network", "ipv4_pool", "must use a 32-bit mask")
	}
	if network.ConfigVersion < 0 {
		return invalid("network", "config_version", "must not be negative")
	}
	if network.CreatedAt.IsZero() {
		return invalid("network", "created_at", "is required")
	}
	return nil
}

// ValidateNode validates a node's public identity and client metadata.
func ValidateNode(node Node) error {
	if err := required("node", "id", node.ID); err != nil {
		return err
	}
	if err := required("node", "display_name", node.DisplayName); err != nil {
		return err
	}
	if err := required("node", "public_key", node.PublicKey); err != nil {
		return err
	}
	if err := required("node", "platform", node.Platform); err != nil {
		return err
	}
	if err := required("node", "client_version", node.ClientVersion); err != nil {
		return err
	}
	return nil
}

// ValidateMembership validates an IPv4 assignment and its role/state.
func ValidateMembership(membership Membership) error {
	if err := required("membership", "network_id", membership.NetworkID); err != nil {
		return err
	}
	if err := required("membership", "node_id", membership.NodeID); err != nil {
		return err
	}
	if membership.VirtualIPv4 == nil || membership.VirtualIPv4.To4() == nil {
		return invalid("membership", "virtual_ipv4", "must be an IPv4 address")
	}
	switch membership.Role {
	case RoleOwner, RoleAdmin, RoleMember:
	default:
		return invalid("membership", "role", "is not supported")
	}
	switch membership.Status {
	case MembershipPending, MembershipActive, MembershipSuspended, MembershipRevoked:
	default:
		return invalid("membership", "status", "is not supported")
	}
	return nil
}

func validateEndpointAddress(model string, address net.IP, family EndpointFamily) error {
	if address == nil {
		return invalid(model, "address", "is required")
	}
	switch family {
	case FamilyIPv4:
		if address.To4() == nil {
			return invalid(model, "address", "must be IPv4 for family ipv4")
		}
	case FamilyIPv6:
		if address.To16() == nil || address.To4() != nil {
			return invalid(model, "address", "must be IPv6 for family ipv6")
		}
	default:
		return invalid(model, "family", "must be ipv4 or ipv6")
	}
	return nil
}

// ValidateEndpointCandidate validates a transport endpoint. Endpoint
// candidates may be IPv4 or IPv6, but the declared family must match.
func ValidateEndpointCandidate(endpoint EndpointCandidate) error {
	if err := required("endpoint_candidate", "node_id", endpoint.NodeID); err != nil {
		return err
	}
	if err := validateEndpointAddress("endpoint_candidate", endpoint.Address, endpoint.Family); err != nil {
		return err
	}
	if endpoint.Port == 0 {
		return invalid("endpoint_candidate", "port", "must be between 1 and 65535")
	}
	if strings.TrimSpace(endpoint.Interface) == "" {
		return invalid("endpoint_candidate", "interface", "is required")
	}
	if endpoint.Priority < 0 {
		return invalid("endpoint_candidate", "priority", "must not be negative")
	}
	if endpoint.ObservedAt.IsZero() {
		return invalid("endpoint_candidate", "observed_at", "is required")
	}
	return nil
}

// ValidateInvite validates the persisted invitation verifier and lifecycle
// timestamps. There is intentionally no raw token field to validate.
func ValidateInvite(invite Invite) error {
	if err := required("invite", "id", invite.ID); err != nil {
		return err
	}
	if err := required("invite", "network_id", invite.NetworkID); err != nil {
		return err
	}
	if err := required("invite", "token_hash", invite.TokenHash); err != nil {
		return err
	}
	if invite.CreatedAt.IsZero() {
		return invalid("invite", "created_at", "is required")
	}
	if invite.ExpiresAt.IsZero() || !invite.ExpiresAt.After(invite.CreatedAt) {
		return invalid("invite", "expires_at", "must be after created_at")
	}
	if invite.ConsumedAt != nil && invite.ConsumedAt.Before(invite.CreatedAt) {
		return invalid("invite", "consumed_at", "must not precede created_at")
	}
	if invite.RevokedAt != nil && invite.RevokedAt.Before(invite.CreatedAt) {
		return invalid("invite", "revoked_at", "must not precede created_at")
	}
	if invite.ConsumedAt != nil && invite.RevokedAt != nil {
		return invalid("invite", "lifecycle", "cannot be both consumed and revoked")
	}
	if invite.ConsumedByNodeID != "" && invite.ConsumedAt == nil {
		return invalid("invite", "consumed_by_node_id", "requires consumed_at")
	}
	return nil
}

// ValidateRelayAssignment validates the current relay endpoint and lifecycle.
func ValidateRelayAssignment(assignment RelayAssignment) error {
	if err := required("relay_assignment", "id", assignment.ID); err != nil {
		return err
	}
	if err := required("relay_assignment", "network_id", assignment.NetworkID); err != nil {
		return err
	}
	if err := required("relay_assignment", "node_id", assignment.NodeID); err != nil {
		return err
	}
	if err := required("relay_assignment", "relay_node_id", assignment.RelayNodeID); err != nil {
		return err
	}
	if assignment.NodeID == assignment.RelayNodeID {
		return invalid("relay_assignment", "relay_node_id", "must differ from node_id")
	}
	if err := validateEndpointAddress("relay_assignment", assignment.Address, assignment.Family); err != nil {
		return err
	}
	if assignment.Port == 0 {
		return invalid("relay_assignment", "port", "must be between 1 and 65535")
	}
	switch assignment.Status {
	case RelayAssignmentActive, RelayAssignmentExpired, RelayAssignmentRevoked:
	default:
		return invalid("relay_assignment", "status", "is not supported")
	}
	if assignment.AssignedAt.IsZero() {
		return invalid("relay_assignment", "assigned_at", "is required")
	}
	if assignment.ExpiresAt != nil && !assignment.ExpiresAt.After(assignment.AssignedAt) {
		return invalid("relay_assignment", "expires_at", "must be after assigned_at")
	}
	return nil
}

// IsFreshEndpoint reports whether an endpoint observation is in the supplied
// inclusive freshness window and is not from the future.
func IsFreshEndpoint(endpoint EndpointCandidate, now time.Time, maxAge time.Duration) bool {
	if maxAge < 0 || endpoint.ObservedAt.IsZero() || endpoint.ObservedAt.After(now) {
		return false
	}
	return !endpoint.ObservedAt.Before(now.Add(-maxAge))
}
