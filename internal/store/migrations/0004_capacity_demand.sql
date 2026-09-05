CREATE TABLE capacity_demand_deliveries (
    pool_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    event_id TEXT NOT NULL,
    payload TEXT NOT NULL,
    observed_since INTEGER NOT NULL,
    attempted_at INTEGER,
    delivered_at INTEGER,
    status_code INTEGER NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (pool_id, event_type)
);
