-- The last lines a runner wrote before it ended in a fault, as a JSON array of
-- at most forty strings. They live on the job rather than the runner because
-- the runner is removed minutes later and the job is what a person asks about;
-- the explanation quotes the line that decided its class from here. Empty
-- means none were kept: a clean exit, or an agent older than this column.
ALTER TABLE jobs ADD COLUMN output_tail TEXT NOT NULL DEFAULT '';
