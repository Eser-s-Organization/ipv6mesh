-- Canonical control-plane schema. No private keys, raw invite tokens, or VPN
-- payloads are stored here.

CREATE TABLE IF NOT EXISTS nodes (
    id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    public_key TEXT NOT NULL UNIQUE,
    platform TEXT NOT NULL,
    client_version TEXT NOT NULL,
    last_seen TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS networks (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    ipv4_pool CIDR NOT NULL CHECK (family(ipv4_pool) = 4),
    owner_id TEXT NOT NULL,
    config_version BIGINT NOT NULL DEFAULT 1 CHECK (config_version > 0),
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS memberships (
    network_id TEXT NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    virtual_ipv4 INET NOT NULL CHECK (family(virtual_ipv4) = 4),
    role TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
    status TEXT NOT NULL CHECK (status IN ('pending', 'active', 'suspended', 'revoked')),
    PRIMARY KEY (network_id, node_id),
    UNIQUE (network_id, virtual_ipv4)
);

CREATE TABLE IF NOT EXISTS invites (
    id TEXT PRIMARY KEY,
    network_id TEXT NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    consumed_by_node_id TEXT REFERENCES nodes(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL,
    CHECK (expires_at > created_at),
    CHECK (consumed_at IS NULL OR consumed_at >= created_at),
    CHECK (revoked_at IS NULL OR revoked_at >= created_at),
    CHECK (consumed_by_node_id IS NULL OR consumed_at IS NOT NULL),
    CHECK (NOT (consumed_at IS NOT NULL AND revoked_at IS NOT NULL))
);

CREATE TABLE IF NOT EXISTS endpoint_candidates (
    id BIGSERIAL PRIMARY KEY,
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    address INET NOT NULL,
    port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
    address_family SMALLINT NOT NULL CHECK (address_family IN (4, 6)),
    interface_name TEXT NOT NULL,
    priority INTEGER NOT NULL DEFAULT 0 CHECK (priority >= 0),
    observed_at TIMESTAMPTZ NOT NULL,
    CHECK ((address_family = 4 AND family(address) = 4) OR
           (address_family = 6 AND family(address) = 6)),
    UNIQUE (node_id, address, port, interface_name)
);

CREATE TABLE IF NOT EXISTS relay_assignments (
    id TEXT PRIMARY KEY,
    network_id TEXT NOT NULL REFERENCES networks(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    relay_node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    address INET NOT NULL,
    port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
    address_family SMALLINT NOT NULL CHECK (address_family IN (4, 6)),
    status TEXT NOT NULL CHECK (status IN ('active', 'expired', 'revoked')),
    assigned_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ,
    CHECK (node_id <> relay_node_id),
    CHECK ((address_family = 4 AND family(address) = 4) OR
           (address_family = 6 AND family(address) = 6)),
    CHECK (expires_at IS NULL OR expires_at > assigned_at),
    UNIQUE (network_id, node_id)
);

CREATE TABLE IF NOT EXISTS audit_events (
    id TEXT PRIMARY KEY,
    network_id TEXT REFERENCES networks(id) ON DELETE SET NULL,
    actor_id TEXT REFERENCES nodes(id) ON DELETE SET NULL,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL
);
