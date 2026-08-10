package db

import (
	"context"
	"errors"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
)

var (
	// ErrNotFound means that the requested control-plane resource does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict is returned for stable uniqueness and duplicate-resource violations.
	ErrConflict = errors.New("conflict")
	// Invite lifecycle errors are separate so callers can distinguish retryable
	// or user-visible invite states without matching strings.
	ErrInviteExpired  = errors.New("invite expired")
	ErrInviteConsumed = errors.New("invite already consumed")
	ErrInviteRevoked  = errors.New("invite revoked")
	// ErrDatabaseUnavailable is used when a PostgreSQL repository was created
	// without an explicitly supplied database handle.
	ErrDatabaseUnavailable = errors.New("database unavailable")
	ErrInvalidContext      = errors.New("invalid context")
)

// DefaultEndpointMaxAge is the freshness window used while building snapshots.
const DefaultEndpointMaxAge = 10 * time.Minute

// Repository is the context-aware control-plane persistence contract. The
// PostgreSQL implementation uses SQL transactions for multi-step mutations;
// the memory implementation provides the same domain semantics for tests.
type Repository interface {
	CreateNetwork(context.Context, control.Network) error
	GetNetwork(context.Context, string) (control.Network, error)
	AddNode(context.Context, control.Node) error
	GetNode(context.Context, string) (control.Node, error)
	RemoveNode(context.Context, string) error
	AddMembership(context.Context, control.Membership) error
	RemoveMembership(context.Context, string, string) error
	CreateInvite(context.Context, control.Invite) error
	ConsumeInvite(context.Context, string, string, time.Time) (control.Invite, error)
	ConsumeInviteForNode(context.Context, string, string, time.Time, string) (control.Invite, error)
	ReplaceEndpoints(context.Context, string, []control.EndpointCandidate) error
	SetRelayAssignment(context.Context, control.RelayAssignment) error
	RemoveRelayAssignment(context.Context, string) error
	BuildSnapshot(context.Context, string, string) (control.NetworkSnapshot, error)
}

// TransactionalRepository is the optional enrollment boundary. Task 3 can
// execute invite consumption, membership insertion, address allocation, and
// version updates through one Repository view and commit them together.
type TransactionalRepository interface {
	Repository
	WithTransaction(context.Context, func(context.Context, Repository) error) error
}

// MemoryRepository is an in-memory implementation of the control-plane
// repository. It is deliberately concurrency-safe and contains no network or
// database dependency.
type MemoryRepository struct {
	mu                    sync.RWMutex
	networks              map[string]control.Network
	nodes                 map[string]control.Node
	publicKeys            map[string]string
	memberships           map[string]map[string]control.Membership
	invites               map[string]control.Invite
	inviteByToken         map[string]string
	endpoints             map[string][]control.EndpointCandidate
	relayAssignments      map[string]map[string]control.RelayAssignment
	relayAssignmentOwners map[string]string
	endpointMaxAge        time.Duration
}

// NewMemoryRepository creates an empty repository using the default endpoint
// freshness policy.
func NewMemoryRepository() *MemoryRepository {
	return NewMemoryRepositoryWithEndpointMaxAge(DefaultEndpointMaxAge)
}

// NewMemoryRepositoryWithEndpointMaxAge is useful for deterministic tests and
// deployments that choose a different endpoint observation policy.
func NewMemoryRepositoryWithEndpointMaxAge(maxAge time.Duration) *MemoryRepository {
	return &MemoryRepository{
		networks:              make(map[string]control.Network),
		nodes:                 make(map[string]control.Node),
		publicKeys:            make(map[string]string),
		memberships:           make(map[string]map[string]control.Membership),
		invites:               make(map[string]control.Invite),
		inviteByToken:         make(map[string]string),
		endpoints:             make(map[string][]control.EndpointCandidate),
		relayAssignments:      make(map[string]map[string]control.RelayAssignment),
		relayAssignmentOwners: make(map[string]string),
		endpointMaxAge:        maxAge,
	}
}

// WithTransaction runs a memory transaction against an isolated state copy
// while holding the repository lock, then publishes the copy only on success.
// This mirrors the commit/rollback shape of the PostgreSQL implementation and
// keeps multi-step tests deterministic without an external service.
func (repository *MemoryRepository) WithTransaction(ctx context.Context, operation func(context.Context, Repository) error) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if operation == nil {
		return control.ErrValidation
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	working := cloneMemoryRepositoryLocked(repository)
	if err := operation(ctx, working); err != nil {
		return err
	}
	repository.networks = working.networks
	repository.nodes = working.nodes
	repository.publicKeys = working.publicKeys
	repository.memberships = working.memberships
	repository.invites = working.invites
	repository.inviteByToken = working.inviteByToken
	repository.endpoints = working.endpoints
	repository.relayAssignments = working.relayAssignments
	repository.relayAssignmentOwners = working.relayAssignmentOwners
	return nil
}

func cloneMemoryRepositoryLocked(source *MemoryRepository) *MemoryRepository {
	clone := NewMemoryRepositoryWithEndpointMaxAge(source.endpointMaxAge)
	for networkID, network := range source.networks {
		clone.networks[networkID] = cloneNetwork(network)
	}
	for nodeID, node := range source.nodes {
		clone.nodes[nodeID] = cloneNode(node)
	}
	for publicKey, nodeID := range source.publicKeys {
		clone.publicKeys[publicKey] = nodeID
	}
	for networkID, members := range source.memberships {
		copiedMembers := make(map[string]control.Membership, len(members))
		for nodeID, membership := range members {
			copiedMembers[nodeID] = cloneMembership(membership)
		}
		clone.memberships[networkID] = copiedMembers
	}
	for inviteID, invite := range source.invites {
		clone.invites[inviteID] = cloneInvite(invite)
	}
	for tokenHash, inviteID := range source.inviteByToken {
		clone.inviteByToken[tokenHash] = inviteID
	}
	for nodeID, endpoints := range source.endpoints {
		clone.endpoints[nodeID] = cloneEndpoints(endpoints)
	}
	for networkID, assignments := range source.relayAssignments {
		copiedAssignments := make(map[string]control.RelayAssignment, len(assignments))
		for nodeID, assignment := range assignments {
			copiedAssignments[nodeID] = cloneRelayAssignment(assignment)
		}
		clone.relayAssignments[networkID] = copiedAssignments
	}
	for assignmentID, owner := range source.relayAssignmentOwners {
		clone.relayAssignmentOwners[assignmentID] = owner
	}
	return clone
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidContext
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func cloneIP(ip net.IP) net.IP {
	if ip == nil {
		return nil
	}
	return append(net.IP(nil), ip...)
}

func cloneNetwork(network control.Network) control.Network {
	return network
}

func cloneNode(node control.Node) control.Node { return node }

func cloneMembership(membership control.Membership) control.Membership {
	membership.VirtualIPv4 = cloneIP(membership.VirtualIPv4)
	return membership
}

func cloneEndpoint(endpoint control.EndpointCandidate) control.EndpointCandidate {
	endpoint.Address = cloneIP(endpoint.Address)
	return endpoint
}

func cloneEndpoints(endpoints []control.EndpointCandidate) []control.EndpointCandidate {
	if endpoints == nil {
		return nil
	}
	cloned := make([]control.EndpointCandidate, len(endpoints))
	for index, endpoint := range endpoints {
		cloned[index] = cloneEndpoint(endpoint)
	}
	return cloned
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneInvite(invite control.Invite) control.Invite {
	invite.ConsumedAt = cloneTime(invite.ConsumedAt)
	invite.RevokedAt = cloneTime(invite.RevokedAt)
	return invite
}

func cloneRelayAssignment(assignment control.RelayAssignment) control.RelayAssignment {
	assignment.Address = cloneIP(assignment.Address)
	assignment.ExpiresAt = cloneTime(assignment.ExpiresAt)
	return assignment
}

func relayAssignmentOwnerKey(networkID, nodeID string) string {
	return networkID + "\x00" + nodeID
}

func (repository *MemoryRepository) deleteRelayAssignmentLocked(networkID, nodeID string) {
	assignments := repository.relayAssignments[networkID]
	assignment, exists := assignments[nodeID]
	if !exists {
		return
	}
	delete(assignments, nodeID)
	delete(repository.relayAssignmentOwners, assignment.ID)
	if len(assignments) == 0 {
		delete(repository.relayAssignments, networkID)
	}
}

type endpointIdentity struct {
	address       string
	port          uint16
	interfaceName string
}

func endpointIdentityFor(endpoint control.EndpointCandidate) endpointIdentity {
	return endpointIdentity{address: endpoint.Address.String(), port: endpoint.Port, interfaceName: endpoint.Interface}
}

func sortEndpointCandidates(endpoints []control.EndpointCandidate) {
	sort.SliceStable(endpoints, func(left, right int) bool {
		if endpoints[left].Priority != endpoints[right].Priority {
			return endpoints[left].Priority < endpoints[right].Priority
		}
		if endpoints[left].Address.String() != endpoints[right].Address.String() {
			return endpoints[left].Address.String() < endpoints[right].Address.String()
		}
		if endpoints[left].Port != endpoints[right].Port {
			return endpoints[left].Port < endpoints[right].Port
		}
		return endpoints[left].Interface < endpoints[right].Interface
	})
}

func sortPeersByNodeID(peers []control.Peer) {
	sort.SliceStable(peers, func(left, right int) bool {
		if peers[left].NodeID != peers[right].NodeID {
			return peers[left].NodeID < peers[right].NodeID
		}
		if peers[left].PublicKey != peers[right].PublicKey {
			return peers[left].PublicKey < peers[right].PublicKey
		}
		return peers[left].VirtualIPv4.String() < peers[right].VirtualIPv4.String()
	})
}

func clonePeer(peer control.Peer) control.Peer {
	peer.Node = cloneNode(peer.Node)
	peer.Membership = cloneMembership(peer.Membership)
	peer.VirtualIPv4 = cloneIP(peer.VirtualIPv4)
	peer.Endpoints = cloneEndpoints(peer.Endpoints)
	return peer
}

func cloneSnapshot(snapshot control.NetworkSnapshot) control.NetworkSnapshot {
	snapshot.LocalVirtualIPv4 = cloneIP(snapshot.LocalVirtualIPv4)
	if snapshot.Peers != nil {
		peers := make([]control.Peer, len(snapshot.Peers))
		for index, peer := range snapshot.Peers {
			peers[index] = clonePeer(peer)
		}
		snapshot.Peers = peers
	}
	if snapshot.RelayAssignment != nil {
		assignment := cloneRelayAssignment(*snapshot.RelayAssignment)
		snapshot.RelayAssignment = &assignment
	}
	if snapshot.Relay != nil {
		assignment := cloneRelayAssignment(*snapshot.Relay)
		snapshot.Relay = &assignment
	}
	return snapshot
}

func (repository *MemoryRepository) incrementNetworkVersionLocked(networkID string) {
	network := repository.networks[networkID]
	if network.ConfigVersion <= 0 {
		network.ConfigVersion = 1
	} else {
		network.ConfigVersion++
	}
	repository.networks[networkID] = network
}

// CreateNetwork persists a new network. Zero config versions are normalized to
// the first published version.
func (repository *MemoryRepository) CreateNetwork(ctx context.Context, network control.Network) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := control.ValidateNetwork(network); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.networks[network.ID]; exists {
		return ErrConflict
	}
	if network.ConfigVersion <= 0 {
		network.ConfigVersion = 1
	}
	repository.networks[network.ID] = cloneNetwork(network)
	return nil
}

// GetNetwork retrieves a defensive copy of a network.
func (repository *MemoryRepository) GetNetwork(ctx context.Context, networkID string) (control.Network, error) {
	if err := contextError(ctx); err != nil {
		return control.Network{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	network, exists := repository.networks[networkID]
	if !exists {
		return control.Network{}, ErrNotFound
	}
	return cloneNetwork(network), nil
}

// AddNode persists a node and enforces public-key uniqueness.
func (repository *MemoryRepository) AddNode(ctx context.Context, node control.Node) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := control.ValidateNode(node); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.nodes[node.ID]; exists {
		return ErrConflict
	}
	if _, exists := repository.publicKeys[node.PublicKey]; exists {
		return ErrConflict
	}
	repository.nodes[node.ID] = cloneNode(node)
	repository.publicKeys[node.PublicKey] = node.ID
	return nil
}

// CreateNode is a descriptive alias for AddNode.
func (repository *MemoryRepository) CreateNode(ctx context.Context, node control.Node) error {
	return repository.AddNode(ctx, node)
}

// GetNode retrieves a defensive copy of a node.
func (repository *MemoryRepository) GetNode(ctx context.Context, nodeID string) (control.Node, error) {
	if err := contextError(ctx); err != nil {
		return control.Node{}, err
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	node, exists := repository.nodes[nodeID]
	if !exists {
		return control.Node{}, ErrNotFound
	}
	return cloneNode(node), nil
}

// RemoveNode removes a node and its dependent control-plane state. Network
// versions are advanced once for each affected network.
func (repository *MemoryRepository) RemoveNode(ctx context.Context, nodeID string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	node, exists := repository.nodes[nodeID]
	if !exists {
		return ErrNotFound
	}
	delete(repository.nodes, nodeID)
	delete(repository.publicKeys, node.PublicKey)
	delete(repository.endpoints, nodeID)
	for inviteID, invite := range repository.invites {
		if invite.ConsumedByNodeID == nodeID {
			invite.ConsumedByNodeID = ""
			repository.invites[inviteID] = cloneInvite(invite)
		}
	}
	affectedNetworks := make(map[string]struct{})
	for networkID, networkMemberships := range repository.memberships {
		if _, member := networkMemberships[nodeID]; member {
			delete(networkMemberships, nodeID)
			affectedNetworks[networkID] = struct{}{}
		}
	}
	for networkID, assignments := range repository.relayAssignments {
		for assignmentNodeID, assignment := range assignments {
			if assignment.NodeID == nodeID || assignment.RelayNodeID == nodeID {
				delete(assignments, assignmentNodeID)
				delete(repository.relayAssignmentOwners, assignment.ID)
				affectedNetworks[networkID] = struct{}{}
			}
		}
		if len(assignments) == 0 {
			delete(repository.relayAssignments, networkID)
		}
	}
	for networkID := range affectedNetworks {
		repository.incrementNetworkVersionLocked(networkID)
	}
	return nil
}

// AddMembership assigns an available virtual IPv4 address within a network.
func (repository *MemoryRepository) AddMembership(ctx context.Context, membership control.Membership) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := control.ValidateMembership(membership); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	network, exists := repository.networks[membership.NetworkID]
	if !exists {
		return ErrNotFound
	}
	if _, exists := repository.nodes[membership.NodeID]; !exists {
		return ErrNotFound
	}
	if err := control.ValidateIPv4InPool(membership.VirtualIPv4, network.IPv4Pool); err != nil {
		return err
	}
	networkMemberships := repository.memberships[membership.NetworkID]
	if networkMemberships == nil {
		networkMemberships = make(map[string]control.Membership)
		repository.memberships[membership.NetworkID] = networkMemberships
	}
	if _, exists := networkMemberships[membership.NodeID]; exists {
		return ErrConflict
	}
	for _, existing := range networkMemberships {
		if existing.VirtualIPv4.Equal(membership.VirtualIPv4) {
			return ErrConflict
		}
	}
	membership.VirtualIPv4 = cloneIP(membership.VirtualIPv4.To4())
	networkMemberships[membership.NodeID] = cloneMembership(membership)
	repository.incrementNetworkVersionLocked(membership.NetworkID)
	return nil
}

// AddMember is an alias retained for callers that use the shorter domain term.
func (repository *MemoryRepository) AddMember(ctx context.Context, membership control.Membership) error {
	return repository.AddMembership(ctx, membership)
}

// RemoveMembership removes a network/node membership.
func (repository *MemoryRepository) RemoveMembership(ctx context.Context, networkID, nodeID string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	networkMemberships, exists := repository.memberships[networkID]
	if !exists {
		return ErrNotFound
	}
	if _, exists := networkMemberships[nodeID]; !exists {
		return ErrNotFound
	}
	delete(networkMemberships, nodeID)
	repository.deleteRelayAssignmentLocked(networkID, nodeID)
	assignments := repository.relayAssignments[networkID]
	for assignmentNodeID, assignment := range assignments {
		if assignment.NodeID == nodeID || assignment.RelayNodeID == nodeID {
			repository.deleteRelayAssignmentLocked(networkID, assignmentNodeID)
		}
	}
	repository.incrementNetworkVersionLocked(networkID)
	return nil
}

// CreateInvite persists a token verifier and lifecycle window.
func (repository *MemoryRepository) CreateInvite(ctx context.Context, invite control.Invite) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := control.ValidateInvite(invite); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.networks[invite.NetworkID]; !exists {
		return ErrNotFound
	}
	if invite.ConsumedByNodeID != "" {
		if _, exists := repository.nodes[invite.ConsumedByNodeID]; !exists {
			return ErrNotFound
		}
	}
	if _, exists := repository.invites[invite.ID]; exists {
		return ErrConflict
	}
	if _, exists := repository.inviteByToken[invite.TokenHash]; exists {
		return ErrConflict
	}
	repository.invites[invite.ID] = cloneInvite(invite)
	repository.inviteByToken[invite.TokenHash] = invite.ID
	return nil
}

// ConsumeInvite is the compatibility wrapper for consumers that do not yet
// identify the enrolling node.
func (repository *MemoryRepository) ConsumeInvite(ctx context.Context, inviteOrNetworkID, tokenHash string, consumedAt time.Time) (control.Invite, error) {
	return repository.ConsumeInviteForNode(ctx, inviteOrNetworkID, tokenHash, consumedAt, "")
}

// ConsumeInviteForNode atomically verifies and consumes one invite while
// recording the node that consumed it when supplied.
func (repository *MemoryRepository) ConsumeInviteForNode(ctx context.Context, inviteOrNetworkID, tokenHash string, consumedAt time.Time, consumedByNodeID string) (control.Invite, error) {
	if err := contextError(ctx); err != nil {
		return control.Invite{}, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	inviteID, direct := repository.inviteByLookupLocked(inviteOrNetworkID, tokenHash)
	if !direct {
		return control.Invite{}, ErrNotFound
	}
	if consumedByNodeID != "" {
		if _, exists := repository.nodes[consumedByNodeID]; !exists {
			return control.Invite{}, ErrNotFound
		}
	}
	if consumedAt.IsZero() {
		consumedAt = time.Now().UTC()
	}
	invite := repository.invites[inviteID]
	if invite.ConsumedAt != nil {
		return control.Invite{}, ErrInviteConsumed
	}
	if invite.RevokedAt != nil {
		return control.Invite{}, ErrInviteRevoked
	}
	if invite.IsExpired(consumedAt) {
		return control.Invite{}, ErrInviteExpired
	}
	if consumedAt.Before(invite.CreatedAt) {
		return control.Invite{}, control.ErrValidation
	}
	invite.ConsumedAt = &consumedAt
	invite.ConsumedByNodeID = consumedByNodeID
	repository.invites[inviteID] = cloneInvite(invite)
	return cloneInvite(invite), nil
}

func (repository *MemoryRepository) inviteByLookupLocked(value, tokenHash string) (string, bool) {
	if invite, exists := repository.invites[value]; exists {
		if invite.TokenHash != tokenHash {
			return "", false
		}
		return value, true
	}
	if inviteID, exists := repository.inviteByToken[tokenHash]; exists {
		invite := repository.invites[inviteID]
		if invite.NetworkID == value {
			return inviteID, true
		}
	}
	return "", false
}

// ReplaceEndpoints atomically replaces all observed endpoints for a node.
func (repository *MemoryRepository) ReplaceEndpoints(ctx context.Context, nodeID string, endpoints []control.EndpointCandidate) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.nodes[nodeID]; !exists {
		return ErrNotFound
	}
	validated := make([]control.EndpointCandidate, len(endpoints))
	seen := make(map[endpointIdentity]struct{}, len(endpoints))
	for index, endpoint := range endpoints {
		if endpoint.NodeID != nodeID {
			return control.ErrValidation
		}
		if err := control.ValidateEndpointCandidate(endpoint); err != nil {
			return err
		}
		validated[index] = cloneEndpoint(endpoint)
		if validated[index].Family == control.FamilyIPv4 {
			validated[index].Address = cloneIP(validated[index].Address.To4())
		}
		identity := endpointIdentityFor(validated[index])
		if _, exists := seen[identity]; exists {
			return ErrConflict
		}
		seen[identity] = struct{}{}
	}
	sortEndpointCandidates(validated)
	repository.endpoints[nodeID] = validated
	for networkID, networkMemberships := range repository.memberships {
		if _, member := networkMemberships[nodeID]; member {
			repository.incrementNetworkVersionLocked(networkID)
		}
	}
	return nil
}

// SetRelayAssignment upserts the current assignment for a network/node pair.
func (repository *MemoryRepository) SetRelayAssignment(ctx context.Context, assignment control.RelayAssignment) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := control.ValidateRelayAssignment(assignment); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.networks[assignment.NetworkID]; !exists {
		return ErrNotFound
	}
	if _, exists := repository.nodes[assignment.NodeID]; !exists {
		return ErrNotFound
	}
	if _, exists := repository.nodes[assignment.RelayNodeID]; !exists {
		return ErrNotFound
	}
	networkMemberships := repository.memberships[assignment.NetworkID]
	if networkMemberships == nil {
		return ErrNotFound
	}
	targetMembership, targetExists := networkMemberships[assignment.NodeID]
	if !targetExists {
		return ErrNotFound
	}
	if targetMembership.Status != control.MembershipActive {
		return control.ErrValidation
	}
	relayMembership, relayExists := networkMemberships[assignment.RelayNodeID]
	if !relayExists {
		return ErrNotFound
	}
	if relayMembership.Status != control.MembershipActive {
		return control.ErrValidation
	}
	assignments := repository.relayAssignments[assignment.NetworkID]
	if assignments == nil {
		assignments = make(map[string]control.RelayAssignment)
		repository.relayAssignments[assignment.NetworkID] = assignments
	}
	ownerKey := relayAssignmentOwnerKey(assignment.NetworkID, assignment.NodeID)
	if existingOwner, exists := repository.relayAssignmentOwners[assignment.ID]; exists && existingOwner != ownerKey {
		return ErrConflict
	}
	if existing, exists := assignments[assignment.NodeID]; exists && existing.ID != assignment.ID {
		delete(repository.relayAssignmentOwners, existing.ID)
	}
	assignments[assignment.NodeID] = cloneRelayAssignment(assignment)
	repository.relayAssignmentOwners[assignment.ID] = ownerKey
	repository.incrementNetworkVersionLocked(assignment.NetworkID)
	return nil
}

// RemoveRelayAssignment removes the current assignment for a network.
func (repository *MemoryRepository) RemoveRelayAssignment(ctx context.Context, networkID string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	assignments, exists := repository.relayAssignments[networkID]
	if !exists || len(assignments) == 0 {
		return ErrNotFound
	}
	for _, assignment := range assignments {
		delete(repository.relayAssignmentOwners, assignment.ID)
	}
	delete(repository.relayAssignments, networkID)
	repository.incrementNetworkVersionLocked(networkID)
	return nil
}

// BuildSnapshot builds a snapshot using the current wall-clock time.
func (repository *MemoryRepository) BuildSnapshot(ctx context.Context, networkID, localNodeID string) (control.NetworkSnapshot, error) {
	return repository.BuildSnapshotAt(ctx, networkID, localNodeID, time.Now().UTC())
}

// BuildVersionedSnapshot is a descriptive alias for BuildSnapshot.
func (repository *MemoryRepository) BuildVersionedSnapshot(ctx context.Context, networkID, localNodeID string) (control.NetworkSnapshot, error) {
	return repository.BuildSnapshot(ctx, networkID, localNodeID)
}

// BuildSnapshotAt makes snapshot freshness deterministic for tests and callers
// that already have a request timestamp.
func (repository *MemoryRepository) BuildSnapshotAt(ctx context.Context, networkID, localNodeID string, now time.Time) (control.NetworkSnapshot, error) {
	if err := contextError(ctx); err != nil {
		return control.NetworkSnapshot{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	network, exists := repository.networks[networkID]
	if !exists {
		return control.NetworkSnapshot{}, ErrNotFound
	}
	networkMemberships := repository.memberships[networkID]
	localMembership, localExists := networkMemberships[localNodeID]
	if !localExists || localMembership.Status != control.MembershipActive {
		return control.NetworkSnapshot{}, ErrNotFound
	}
	if _, localNodeExists := repository.nodes[localNodeID]; !localNodeExists {
		return control.NetworkSnapshot{}, ErrNotFound
	}

	memberIDs := make([]string, 0, len(networkMemberships))
	for nodeID := range networkMemberships {
		memberIDs = append(memberIDs, nodeID)
	}
	sort.Strings(memberIDs)
	peers := make([]control.Peer, 0, len(memberIDs)-1)
	for _, nodeID := range memberIDs {
		if nodeID == localNodeID {
			continue
		}
		membership := networkMemberships[nodeID]
		if membership.Status != control.MembershipActive {
			continue
		}
		node, nodeExists := repository.nodes[nodeID]
		if !nodeExists {
			continue
		}
		freshEndpoints := make([]control.EndpointCandidate, 0, len(repository.endpoints[nodeID]))
		for _, endpoint := range repository.endpoints[nodeID] {
			if control.IsFreshEndpoint(endpoint, now, repository.endpointMaxAge) {
				freshEndpoints = append(freshEndpoints, cloneEndpoint(endpoint))
			}
		}
		sortEndpointCandidates(freshEndpoints)
		peers = append(peers, control.Peer{
			NodeID:      node.ID,
			DisplayName: node.DisplayName,
			PublicKey:   node.PublicKey,
			VirtualIPv4: cloneIP(membership.VirtualIPv4),
			Node:        cloneNode(node),
			Membership:  cloneMembership(membership),
			Endpoints:   freshEndpoints,
		})
	}
	sortPeersByNodeID(peers)

	var relay *control.RelayAssignment
	if assignments := repository.relayAssignments[networkID]; len(assignments) > 0 {
		selected, selectedExists := assignments[localNodeID]
		if selectedExists && selected.Status == control.RelayAssignmentActive && relayAssignmentUsable(selected, now) &&
			repository.membershipIsActiveLocked(networkID, selected.NodeID) &&
			repository.membershipIsActiveLocked(networkID, selected.RelayNodeID) {
			assignment := cloneRelayAssignment(selected)
			relay = &assignment
		}
	}

	snapshot := control.NetworkSnapshot{
		NetworkID:        network.ID,
		Generation:       network.ConfigVersion,
		ConfigVersion:    network.ConfigVersion,
		LocalNodeID:      localNodeID,
		LocalVirtualIPv4: cloneIP(localMembership.VirtualIPv4),
		Peers:            peers,
		RelayAssignment:  relay,
		GeneratedAt:      now,
	}
	if relay != nil {
		relayCopy := cloneRelayAssignment(*relay)
		snapshot.Relay = &relayCopy
	}
	return cloneSnapshot(snapshot), nil
}

func (repository *MemoryRepository) membershipIsActiveLocked(networkID, nodeID string) bool {
	membership, exists := repository.memberships[networkID][nodeID]
	return exists && membership.Status == control.MembershipActive
}

// GetSnapshotAt is an alias for callers that use retrieval terminology.
func (repository *MemoryRepository) GetSnapshotAt(ctx context.Context, networkID, localNodeID string, now time.Time) (control.NetworkSnapshot, error) {
	return repository.BuildSnapshotAt(ctx, networkID, localNodeID, now)
}

func relayAssignmentUsable(assignment control.RelayAssignment, now time.Time) bool {
	if assignment.AssignedAt.After(now) {
		return false
	}
	return assignment.ExpiresAt == nil || now.Before(*assignment.ExpiresAt)
}

// The following aliases make the concrete memory repository convenient while
// keeping the Repository interface focused on one clear name per operation.
func (repository *MemoryRepository) DeleteNode(ctx context.Context, nodeID string) error {
	return repository.RemoveNode(ctx, nodeID)
}

func (repository *MemoryRepository) DeleteMembership(ctx context.Context, networkID, nodeID string) error {
	return repository.RemoveMembership(ctx, networkID, nodeID)
}
