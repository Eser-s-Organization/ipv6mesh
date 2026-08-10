package db

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"sort"
	"time"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
)

// SQLExecutor is the small database/sql surface used by the repository. Both
// *sql.DB and *sql.Tx implement it, which keeps query paths injectable without
// introducing a driver or a database protocol of our own.
type SQLExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// PostgresRepository is a PostgreSQL-backed Repository. It never opens a
// connection itself; callers provide a configured *sql.DB and register their
// preferred PostgreSQL driver before constructing it.
type PostgresRepository struct {
	db   *sql.DB
	exec SQLExecutor
}

const (
	removeNodeAffectedNetworksQuery = `
		SELECT DISTINCT network_id
		FROM (
			SELECT network_id FROM memberships WHERE node_id = $1
			UNION
			SELECT network_id FROM relay_assignments
			WHERE node_id = $1 OR relay_node_id = $1
		) AS affected_networks`
	removeNodeDeleteQuery        = `DELETE FROM nodes WHERE id = $1`
	incrementNetworkVersionQuery = `
		UPDATE networks SET config_version = config_version + 1 WHERE id = $1`
	relayMembershipExistsQuery = `
		SELECT EXISTS (
			SELECT 1 FROM memberships
			WHERE network_id = $1 AND node_id = $2 AND status = 'active'
		)`
	snapshotRelayAssignmentsQuery = `
		SELECT relay.id, relay.network_id, relay.node_id, relay.relay_node_id,
		       relay.address::text, relay.port, relay.address_family,
		       relay.status, relay.assigned_at, relay.expires_at
		FROM relay_assignments AS relay
		JOIN memberships AS target_membership
		  ON target_membership.network_id = relay.network_id
		 AND target_membership.node_id = relay.node_id
		JOIN memberships AS relay_membership
		  ON relay_membership.network_id = relay.network_id
		 AND relay_membership.node_id = relay.relay_node_id
		WHERE relay.network_id = $1
		  AND relay.node_id = $2
		  AND relay.status = 'active'
		  AND target_membership.status = 'active'
		  AND relay_membership.status = 'active'`
)

func removeNodeMutationQueries() []string {
	return []string{
		removeNodeAffectedNetworksQuery,
		removeNodeDeleteQuery,
		incrementNetworkVersionQuery,
	}
}

func snapshotReadTransactionOptions() sql.TxOptions {
	return sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
}

// NewPostgresRepository creates a repository around an existing *sql.DB. A
// nil handle is accepted so configuration can construct the object before
// dependency wiring; operations then return ErrDatabaseUnavailable.
func NewPostgresRepository(database *sql.DB) *PostgresRepository {
	if database == nil {
		return &PostgresRepository{}
	}
	return &PostgresRepository{db: database, exec: database}
}

// NewPostgresRepositoryWithExecutor is intended for SQL-boundary tests and
// adapters that expose the database/sql executor surface. A *sql.DB should be
// used in production when transactional mutations are required.
func NewPostgresRepositoryWithExecutor(executor SQLExecutor) *PostgresRepository {
	return &PostgresRepository{exec: executor}
}

func (repository *PostgresRepository) executor(ctx context.Context) (SQLExecutor, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if repository == nil || repository.exec == nil {
		return nil, ErrDatabaseUnavailable
	}
	return repository.exec, nil
}

func (repository *PostgresRepository) withTransaction(ctx context.Context, operation func(SQLExecutor) error) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if repository == nil || repository.exec == nil {
		return ErrDatabaseUnavailable
	}
	if repository.db == nil {
		return operation(repository.exec)
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := operation(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (repository *PostgresRepository) readTransaction(ctx context.Context, operation func(SQLExecutor) error) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if repository == nil || repository.exec == nil {
		return ErrDatabaseUnavailable
	}
	if repository.db == nil {
		return operation(repository.exec)
	}
	options := snapshotReadTransactionOptions()
	tx, err := repository.db.BeginTx(ctx, &options)
	if err != nil {
		return err
	}
	if err := operation(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// WithTransaction exposes a Repository view backed by one PostgreSQL
// transaction. A repository built with only an injected executor can still be
// used for SQL-boundary tests, but callers needing atomicity must construct it
// with NewPostgresRepository and a *sql.DB.
func (repository *PostgresRepository) WithTransaction(ctx context.Context, operation func(context.Context, Repository) error) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if operation == nil {
		return control.ErrValidation
	}
	if repository == nil || repository.exec == nil {
		return ErrDatabaseUnavailable
	}
	if repository.db == nil {
		return operation(ctx, &PostgresRepository{exec: repository.exec})
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	transactionRepository := &PostgresRepository{exec: tx}
	if err := operation(ctx, transactionRepository); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// CreateNetwork inserts a network and returns ErrConflict for any uniqueness
// collision without requiring a driver-specific error type.
func (repository *PostgresRepository) CreateNetwork(ctx context.Context, network control.Network) error {
	if err := control.ValidateNetwork(network); err != nil {
		return err
	}
	executor, err := repository.executor(ctx)
	if err != nil {
		return err
	}
	version := network.ConfigVersion
	if version == 0 {
		version = 1
	}
	result, err := executor.ExecContext(ctx, `
		INSERT INTO networks (id, name, ipv4_pool, owner_id, config_version, created_at)
		VALUES ($1, $2, $3::cidr, $4, $5, $6)
		ON CONFLICT DO NOTHING`,
		network.ID, network.Name, network.IPv4Pool, network.OwnerID, version, network.CreatedAt)
	if err != nil {
		return err
	}
	return rowsAffectedConflict(result)
}

func rowsAffectedConflict(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrConflict
	}
	return nil
}

// GetNetwork loads one network using a parameterized query.
func (repository *PostgresRepository) GetNetwork(ctx context.Context, networkID string) (control.Network, error) {
	executor, err := repository.executor(ctx)
	if err != nil {
		return control.Network{}, err
	}
	return scanNetworkRow(executor.QueryRowContext(ctx, `
		SELECT id, name, ipv4_pool::text, owner_id, config_version, created_at
		FROM networks WHERE id = $1`, networkID))
}

// AddNode inserts a node and maps duplicate IDs/public keys to ErrConflict.
func (repository *PostgresRepository) AddNode(ctx context.Context, node control.Node) error {
	if err := control.ValidateNode(node); err != nil {
		return err
	}
	executor, err := repository.executor(ctx)
	if err != nil {
		return err
	}
	result, err := executor.ExecContext(ctx, `
		INSERT INTO nodes (id, display_name, public_key, platform, client_version, last_seen)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT DO NOTHING`,
		node.ID, node.DisplayName, node.PublicKey, node.Platform, node.ClientVersion, nullableTime(node.LastSeen))
	if err != nil {
		return err
	}
	return rowsAffectedConflict(result)
}

// CreateNode is a descriptive alias for AddNode.
func (repository *PostgresRepository) CreateNode(ctx context.Context, node control.Node) error {
	return repository.AddNode(ctx, node)
}

// GetNode loads one node without exposing any private key field.
func (repository *PostgresRepository) GetNode(ctx context.Context, nodeID string) (control.Node, error) {
	executor, err := repository.executor(ctx)
	if err != nil {
		return control.Node{}, err
	}
	return scanNodeRow(executor.QueryRowContext(ctx, `
		SELECT id, display_name, public_key, platform, client_version, last_seen
		FROM nodes WHERE id = $1`, nodeID))
}

// RemoveNode deletes a node and advances every network affected by its
// membership or relay assignments in the same transaction.
func (repository *PostgresRepository) RemoveNode(ctx context.Context, nodeID string) error {
	return repository.withTransaction(ctx, func(executor SQLExecutor) error {
		networkIDs, err := queryAffectedNetworkIDs(ctx, executor, nodeID)
		if err != nil {
			return err
		}
		result, err := executor.ExecContext(ctx, removeNodeDeleteQuery, nodeID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return ErrNotFound
		}
		for _, networkID := range networkIDs {
			if err := incrementNetworkVersion(ctx, executor, networkID); err != nil {
				return err
			}
		}
		return nil
	})
}

func queryAffectedNetworkIDs(ctx context.Context, executor SQLExecutor, nodeID string) ([]string, error) {
	rows, err := executor.QueryContext(ctx, removeNodeAffectedNetworksQuery, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	networkIDs := make([]string, 0)
	for rows.Next() {
		var networkID string
		if err := rows.Scan(&networkID); err != nil {
			return nil, err
		}
		networkIDs = append(networkIDs, networkID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(networkIDs)
	return networkIDs, nil
}

func (repository *PostgresRepository) DeleteNode(ctx context.Context, nodeID string) error {
	return repository.RemoveNode(ctx, nodeID)
}

// AddMembership inserts a unique network/node assignment and advances the
// network config version in the same transaction when a real *sql.DB is used.
func (repository *PostgresRepository) AddMembership(ctx context.Context, membership control.Membership) error {
	if err := control.ValidateMembership(membership); err != nil {
		return err
	}
	return repository.withTransaction(ctx, func(executor SQLExecutor) error {
		if err := ensureNetworkExists(ctx, executor, membership.NetworkID); err != nil {
			return err
		}
		if err := ensureNodeExists(ctx, executor, membership.NodeID); err != nil {
			return err
		}
		result, err := executor.ExecContext(ctx, `
			INSERT INTO memberships (network_id, node_id, virtual_ipv4, role, status)
			VALUES ($1, $2, $3::inet, $4, $5)
			ON CONFLICT DO NOTHING`,
			membership.NetworkID, membership.NodeID, membership.VirtualIPv4.To4().String(), membership.Role, membership.Status)
		if err != nil {
			return err
		}
		if err := rowsAffectedConflict(result); err != nil {
			return err
		}
		return incrementNetworkVersion(ctx, executor, membership.NetworkID)
	})
}

func (repository *PostgresRepository) AddMember(ctx context.Context, membership control.Membership) error {
	return repository.AddMembership(ctx, membership)
}

// RemoveMembership deletes one assignment and advances the owning network's
// config version.
func (repository *PostgresRepository) RemoveMembership(ctx context.Context, networkID, nodeID string) error {
	return repository.withTransaction(ctx, func(executor SQLExecutor) error {
		result, err := executor.ExecContext(ctx, `
			DELETE FROM memberships WHERE network_id = $1 AND node_id = $2`, networkID, nodeID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return ErrNotFound
		}
		if _, err := executor.ExecContext(ctx, `
			DELETE FROM relay_assignments
			WHERE network_id = $1 AND (node_id = $2 OR relay_node_id = $2)`, networkID, nodeID); err != nil {
			return err
		}
		return incrementNetworkVersion(ctx, executor, networkID)
	})
}

func (repository *PostgresRepository) DeleteMembership(ctx context.Context, networkID, nodeID string) error {
	return repository.RemoveMembership(ctx, networkID, nodeID)
}

func incrementNetworkVersion(ctx context.Context, executor SQLExecutor, networkID string) error {
	result, err := executor.ExecContext(ctx, incrementNetworkVersionQuery, networkID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateInvite inserts only token_hash and lifecycle timestamps.
func (repository *PostgresRepository) CreateInvite(ctx context.Context, invite control.Invite) error {
	if err := control.ValidateInvite(invite); err != nil {
		return err
	}
	return repository.withTransaction(ctx, func(executor SQLExecutor) error {
		if err := ensureNetworkExists(ctx, executor, invite.NetworkID); err != nil {
			return err
		}
		if invite.ConsumedByNodeID != "" {
			if err := ensureNodeExists(ctx, executor, invite.ConsumedByNodeID); err != nil {
				return err
			}
		}
		result, err := executor.ExecContext(ctx, `
			INSERT INTO invites (id, network_id, token_hash, expires_at, consumed_at, revoked_at, consumed_by_node_id, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8)
			ON CONFLICT DO NOTHING`,
			invite.ID, invite.NetworkID, invite.TokenHash, invite.ExpiresAt, invite.ConsumedAt, invite.RevokedAt, invite.ConsumedByNodeID, invite.CreatedAt)
		if err != nil {
			return err
		}
		return rowsAffectedConflict(result)
	})
}

// ConsumeInvite uses one conditional UPDATE as the atomic single-use gate,
// then classifies a failed attempt by reading the persisted lifecycle state.
func (repository *PostgresRepository) ConsumeInvite(ctx context.Context, inviteOrNetworkID, tokenHash string, consumedAt time.Time) (control.Invite, error) {
	if consumedAt.IsZero() {
		consumedAt = time.Now().UTC()
	}
	executor, err := repository.executor(ctx)
	if err != nil {
		return control.Invite{}, err
	}
	const consumeQuery = `
		UPDATE invites
		SET consumed_at = $3
		WHERE (id = $1 OR network_id = $1)
		  AND token_hash = $2
		  AND consumed_at IS NULL
		  AND revoked_at IS NULL
		  AND $3 >= created_at
		  AND expires_at > $3
		RETURNING id, network_id, token_hash, expires_at, consumed_at, revoked_at, consumed_by_node_id, created_at`
	invite, err := scanInviteRow(executor.QueryRowContext(ctx, consumeQuery, inviteOrNetworkID, tokenHash, consumedAt))
	if err == nil {
		return invite, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return control.Invite{}, err
	}
	state, stateErr := scanInviteRow(executor.QueryRowContext(ctx, `
		SELECT id, network_id, token_hash, expires_at, consumed_at, revoked_at, consumed_by_node_id, created_at
		FROM invites WHERE (id = $1 OR network_id = $1) AND token_hash = $2`, inviteOrNetworkID, tokenHash))
	if errors.Is(stateErr, sql.ErrNoRows) {
		return control.Invite{}, ErrNotFound
	}
	if stateErr != nil {
		return control.Invite{}, stateErr
	}
	if state.ConsumedAt != nil {
		return control.Invite{}, ErrInviteConsumed
	}
	if state.RevokedAt != nil {
		return control.Invite{}, ErrInviteRevoked
	}
	if consumedAt.Before(state.CreatedAt) {
		return control.Invite{}, control.ErrValidation
	}
	if state.IsExpired(consumedAt) {
		return control.Invite{}, ErrInviteExpired
	}
	return control.Invite{}, ErrConflict
}

// ReplaceEndpoints deletes and reinserts a node's endpoint candidates in one
// transaction, then advances every network containing that node.
func (repository *PostgresRepository) ReplaceEndpoints(ctx context.Context, nodeID string, endpoints []control.EndpointCandidate) error {
	for _, endpoint := range endpoints {
		if endpoint.NodeID != nodeID {
			return control.ErrValidation
		}
		if err := control.ValidateEndpointCandidate(endpoint); err != nil {
			return err
		}
	}
	return repository.withTransaction(ctx, func(executor SQLExecutor) error {
		if err := ensureNodeExists(ctx, executor, nodeID); err != nil {
			return err
		}
		if _, err := executor.ExecContext(ctx, `DELETE FROM endpoint_candidates WHERE node_id = $1`, nodeID); err != nil {
			return err
		}
		for _, endpoint := range endpoints {
			if _, err := executor.ExecContext(ctx, `
				INSERT INTO endpoint_candidates
					(node_id, address, port, address_family, interface_name, priority, observed_at)
				VALUES ($1, $2::inet, $3, $4, $5, $6, $7)`,
				endpoint.NodeID, endpoint.Address.String(), endpoint.Port, endpointFamilyNumber(endpoint.Family), endpoint.Interface, endpoint.Priority, endpoint.ObservedAt); err != nil {
				return err
			}
		}
		_, err := executor.ExecContext(ctx, `
			UPDATE networks SET config_version = config_version + 1
			WHERE id IN (SELECT network_id FROM memberships WHERE node_id = $1)`, nodeID)
		return err
	})
}

func ensureNodeExists(ctx context.Context, executor SQLExecutor, nodeID string) error {
	var exists bool
	if err := executor.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM nodes WHERE id = $1)`, nodeID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

func ensureNetworkExists(ctx context.Context, executor SQLExecutor, networkID string) error {
	var exists bool
	if err := executor.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM networks WHERE id = $1)`, networkID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

// SetRelayAssignment upserts one assignment and advances the network version.
func (repository *PostgresRepository) SetRelayAssignment(ctx context.Context, assignment control.RelayAssignment) error {
	if err := control.ValidateRelayAssignment(assignment); err != nil {
		return err
	}
	return repository.withTransaction(ctx, func(executor SQLExecutor) error {
		if err := ensureMembershipExists(ctx, executor, assignment.NetworkID, assignment.NodeID); err != nil {
			return err
		}
		if err := ensureMembershipExists(ctx, executor, assignment.NetworkID, assignment.RelayNodeID); err != nil {
			return err
		}
		_, err := executor.ExecContext(ctx, `
			INSERT INTO relay_assignments
				(id, network_id, node_id, relay_node_id, address, port, address_family, status, assigned_at, expires_at)
			VALUES ($1, $2, $3, $4, $5::inet, $6, $7, $8, $9, $10)
			ON CONFLICT (network_id, node_id) DO UPDATE SET
				id = EXCLUDED.id,
				relay_node_id = EXCLUDED.relay_node_id,
				address = EXCLUDED.address,
				port = EXCLUDED.port,
				address_family = EXCLUDED.address_family,
				status = EXCLUDED.status,
				assigned_at = EXCLUDED.assigned_at,
				expires_at = EXCLUDED.expires_at`,
			assignment.ID, assignment.NetworkID, assignment.NodeID, assignment.RelayNodeID, assignment.Address.String(), assignment.Port, endpointFamilyNumber(assignment.Family), assignment.Status, assignment.AssignedAt, assignment.ExpiresAt)
		if err != nil {
			return err
		}
		return incrementNetworkVersion(ctx, executor, assignment.NetworkID)
	})
}

func ensureMembershipExists(ctx context.Context, executor SQLExecutor, networkID, nodeID string) error {
	var exists bool
	if err := executor.QueryRowContext(ctx, relayMembershipExistsQuery, networkID, nodeID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

// RemoveRelayAssignment removes all current assignments for a network.
func (repository *PostgresRepository) RemoveRelayAssignment(ctx context.Context, networkID string) error {
	return repository.withTransaction(ctx, func(executor SQLExecutor) error {
		result, err := executor.ExecContext(ctx, `DELETE FROM relay_assignments WHERE network_id = $1`, networkID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return ErrNotFound
		}
		return incrementNetworkVersion(ctx, executor, networkID)
	})
}

// BuildSnapshot reads the current versioned snapshot at the current time.
func (repository *PostgresRepository) BuildSnapshot(ctx context.Context, networkID, localNodeID string) (control.NetworkSnapshot, error) {
	return repository.BuildSnapshotAt(ctx, networkID, localNodeID, time.Now().UTC())
}

func (repository *PostgresRepository) BuildVersionedSnapshot(ctx context.Context, networkID, localNodeID string) (control.NetworkSnapshot, error) {
	return repository.BuildSnapshot(ctx, networkID, localNodeID)
}

// BuildSnapshotAt uses a repeatable-read, read-only transaction for a stable
// multi-query read when a *sql.DB is available. Endpoint staleness is
// evaluated in Go so the same policy is shared by the memory implementation.
func (repository *PostgresRepository) BuildSnapshotAt(ctx context.Context, networkID, localNodeID string, now time.Time) (control.NetworkSnapshot, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var snapshot control.NetworkSnapshot
	err := repository.readTransaction(ctx, func(executor SQLExecutor) error {
		var err error
		snapshot, err = buildSnapshotWithExecutor(ctx, executor, networkID, localNodeID, now)
		return err
	})
	if err != nil {
		return control.NetworkSnapshot{}, err
	}
	return snapshot, nil
}

func buildSnapshotWithExecutor(ctx context.Context, executor SQLExecutor, networkID, localNodeID string, now time.Time) (control.NetworkSnapshot, error) {
	network, err := scanNetworkRow(executor.QueryRowContext(ctx, `
		SELECT id, name, ipv4_pool::text, owner_id, config_version, created_at
		FROM networks WHERE id = $1`, networkID))
	if errors.Is(err, sql.ErrNoRows) {
		return control.NetworkSnapshot{}, ErrNotFound
	}
	if err != nil {
		return control.NetworkSnapshot{}, err
	}

	rows, err := executor.QueryContext(ctx, `
		SELECT m.network_id, m.node_id, m.virtual_ipv4::text, m.role, m.status,
		       n.id, n.display_name, n.public_key, n.platform, n.client_version, n.last_seen
		FROM memberships AS m
		JOIN nodes AS n ON n.id = m.node_id
		WHERE m.network_id = $1 AND m.status = 'active'
		ORDER BY m.node_id`, networkID)
	if err != nil {
		return control.NetworkSnapshot{}, err
	}
	defer rows.Close()

	var localMembership control.Membership
	localFound := false
	peers := make([]control.Peer, 0)
	for rows.Next() {
		membership, node, scanErr := scanMembershipNodeRow(rows)
		if scanErr != nil {
			return control.NetworkSnapshot{}, scanErr
		}
		if node.ID == localNodeID {
			localMembership = membership
			localFound = true
			continue
		}
		endpoints, endpointErr := readFreshEndpoints(ctx, executor, node.ID, now)
		if endpointErr != nil {
			return control.NetworkSnapshot{}, endpointErr
		}
		peers = append(peers, control.Peer{
			NodeID:      node.ID,
			DisplayName: node.DisplayName,
			PublicKey:   node.PublicKey,
			VirtualIPv4: cloneIP(membership.VirtualIPv4),
			Node:        node,
			Membership:  membership,
			Endpoints:   endpoints,
		})
	}
	if err := rows.Err(); err != nil {
		return control.NetworkSnapshot{}, err
	}
	if !localFound {
		return control.NetworkSnapshot{}, ErrNotFound
	}

	var relay *control.RelayAssignment
	relayRows, err := executor.QueryContext(ctx, snapshotRelayAssignmentsQuery, networkID, localNodeID)
	if err != nil {
		return control.NetworkSnapshot{}, err
	}
	for relayRows.Next() {
		assignment, scanErr := scanRelayAssignmentRow(relayRows)
		if scanErr != nil {
			relayRows.Close()
			return control.NetworkSnapshot{}, scanErr
		}
		if !relayAssignmentUsable(assignment, now) {
			continue
		}
		if relay == nil {
			copyAssignment := cloneRelayAssignment(assignment)
			relay = &copyAssignment
		}
	}
	if err := relayRows.Err(); err != nil {
		relayRows.Close()
		return control.NetworkSnapshot{}, err
	}
	relayRows.Close()

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
		copyAssignment := cloneRelayAssignment(*relay)
		snapshot.Relay = &copyAssignment
	}
	return cloneSnapshot(snapshot), nil
}

func readFreshEndpoints(ctx context.Context, executor SQLExecutor, nodeID string, now time.Time) ([]control.EndpointCandidate, error) {
	rows, err := executor.QueryContext(ctx, `
		SELECT node_id, address::text, port, address_family, interface_name, priority, observed_at
		FROM endpoint_candidates WHERE node_id = $1
		ORDER BY priority, address::text, port`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	endpoints := make([]control.EndpointCandidate, 0)
	for rows.Next() {
		endpoint, scanErr := scanEndpointRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if control.IsFreshEndpoint(endpoint, now, DefaultEndpointMaxAge) {
			endpoints = append(endpoints, endpoint)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return endpoints, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanNetworkRow(row rowScanner) (control.Network, error) {
	var (
		network  control.Network
		poolText string
	)
	if err := row.Scan(&network.ID, &network.Name, &poolText, &network.OwnerID, &network.ConfigVersion, &network.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return control.Network{}, ErrNotFound
		}
		return control.Network{}, err
	}
	poolIP, pool, err := net.ParseCIDR(poolText)
	if err != nil || poolIP.To4() == nil {
		if err == nil {
			err = errors.New("network pool is not IPv4")
		}
		return control.Network{}, err
	}
	if _, bits := pool.Mask.Size(); bits != 32 {
		return control.Network{}, errors.New("network pool is not a 32-bit IPv4 network")
	}
	network.IPv4Pool = poolText
	return network, nil
}

func scanNodeRow(row rowScanner) (control.Node, error) {
	var (
		node     control.Node
		lastSeen sql.NullTime
	)
	if err := row.Scan(&node.ID, &node.DisplayName, &node.PublicKey, &node.Platform, &node.ClientVersion, &lastSeen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return control.Node{}, ErrNotFound
		}
		return control.Node{}, err
	}
	if lastSeen.Valid {
		node.LastSeen = lastSeen.Time
	}
	return node, nil
}

func scanMembershipNodeRow(row rowScanner) (control.Membership, control.Node, error) {
	var (
		membership control.Membership
		node       control.Node
		virtualIP  string
		lastSeen   sql.NullTime
	)
	if err := row.Scan(
		&membership.NetworkID,
		&membership.NodeID,
		&virtualIP,
		&membership.Role,
		&membership.Status,
		&node.ID,
		&node.DisplayName,
		&node.PublicKey,
		&node.Platform,
		&node.ClientVersion,
		&lastSeen,
	); err != nil {
		return control.Membership{}, control.Node{}, err
	}
	if lastSeen.Valid {
		node.LastSeen = lastSeen.Time
	}
	ip := net.ParseIP(virtualIP).To4()
	if ip == nil {
		return control.Membership{}, control.Node{}, errors.New("membership virtual address is not IPv4")
	}
	membership.VirtualIPv4 = ip
	return membership, node, nil
}

func scanEndpointRow(row rowScanner) (control.EndpointCandidate, error) {
	var (
		endpoint      control.EndpointCandidate
		addressText   string
		port          int64
		addressFamily int64
	)
	if err := row.Scan(&endpoint.NodeID, &addressText, &port, &addressFamily, &endpoint.Interface, &endpoint.Priority, &endpoint.ObservedAt); err != nil {
		return control.EndpointCandidate{}, err
	}
	endpoint.Address = net.ParseIP(addressText)
	endpoint.Port = uint16(port)
	switch addressFamily {
	case 4:
		endpoint.Family = control.FamilyIPv4
	case 6:
		endpoint.Family = control.FamilyIPv6
	default:
		return control.EndpointCandidate{}, errors.New("unsupported endpoint address family")
	}
	return endpoint, nil
}

func scanInviteRow(row rowScanner) (control.Invite, error) {
	var (
		invite       control.Invite
		consumedAt   sql.NullTime
		revokedAt    sql.NullTime
		consumedNode sql.NullString
	)
	if err := row.Scan(&invite.ID, &invite.NetworkID, &invite.TokenHash, &invite.ExpiresAt, &consumedAt, &revokedAt, &consumedNode, &invite.CreatedAt); err != nil {
		return control.Invite{}, err
	}
	if consumedAt.Valid {
		invite.ConsumedAt = &consumedAt.Time
	}
	if revokedAt.Valid {
		invite.RevokedAt = &revokedAt.Time
	}
	if consumedNode.Valid {
		invite.ConsumedByNodeID = consumedNode.String
	}
	return invite, nil
}

func scanRelayAssignmentRow(row rowScanner) (control.RelayAssignment, error) {
	var (
		assignment    control.RelayAssignment
		addressText   string
		port          int64
		addressFamily int64
		expiresAt     sql.NullTime
	)
	if err := row.Scan(&assignment.ID, &assignment.NetworkID, &assignment.NodeID, &assignment.RelayNodeID, &addressText, &port, &addressFamily, &assignment.Status, &assignment.AssignedAt, &expiresAt); err != nil {
		return control.RelayAssignment{}, err
	}
	assignment.Address = net.ParseIP(addressText)
	assignment.Port = uint16(port)
	switch addressFamily {
	case 4:
		assignment.Family = control.FamilyIPv4
	case 6:
		assignment.Family = control.FamilyIPv6
	default:
		return control.RelayAssignment{}, errors.New("unsupported relay address family")
	}
	if expiresAt.Valid {
		assignment.ExpiresAt = &expiresAt.Time
	}
	return assignment, nil
}

func endpointFamilyNumber(family control.EndpointFamily) int {
	if family == control.FamilyIPv4 {
		return 4
	}
	return 6
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
