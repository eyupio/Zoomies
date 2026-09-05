ALTER TABLE pools ADD COLUMN cache TEXT NOT NULL DEFAULT '{"enabled":false,"scope":"pool"}';
