-- Which providers a pool will let rent machines for it: the pool's half of an
-- agreement whose other half is providers.pool_selector, in the way a pool's
-- host_selector is its half of the agreement with a host's labels. Empty means
-- any provider, which is what every pool already meant.
ALTER TABLE pools ADD COLUMN provider_selector TEXT NOT NULL DEFAULT '{}';
