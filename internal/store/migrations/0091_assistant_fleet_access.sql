-- Whether the assistant may read this fleet through a provider.
--
-- It is a column on the provider and not one switch for the assistant, because
-- the question is where the data goes: a model on the operator's own machine and
-- a hosted one are different answers, and an operator may well want Eli to read
-- the fleet through the first and never the second. It is off for every provider,
-- including those that already exist, so an upgrade sends nothing anywhere it
-- did not send before.
ALTER TABLE assistant_providers ADD COLUMN fleet_access INTEGER NOT NULL DEFAULT 0;
