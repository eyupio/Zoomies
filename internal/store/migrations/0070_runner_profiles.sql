-- Runner sizes that follow the machine they run on.
--
-- runner_profile is an operator's answer, per host, to "how big is a runner
-- here?": a minimum, a standard size, and the most CPU a runner on the host may
-- be lent. It is one JSON document in one column, like usage and throttle,
-- because it is read and written whole and never searched by a field.
--
-- '{}' is "no profile", and it is every host until an operator writes one. It
-- changes nothing: a host without a profile is sized by the fleet's settings
-- and its slot count exactly as it was before this migration.
--
-- size_from_profile marks a pool whose runners are sized by the host they land
-- on, rather than by typed figures or by one slot's share of the machine. 0 is
-- every pool that exists today, so the upgrade moves none of them.
ALTER TABLE hosts ADD COLUMN runner_profile TEXT NOT NULL DEFAULT '{}';
ALTER TABLE pools ADD COLUMN size_from_profile INTEGER NOT NULL DEFAULT 0;
