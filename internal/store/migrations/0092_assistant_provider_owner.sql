-- Whose a provider is.
--
-- A provider that is somebody's own subscription, used through the vendor's own
-- tool on the controller's machine, may be used only by that person: the vendors'
-- terms are that each person uses their own plan, and a column the rows carry is
-- what lets the controller refuse everybody else. It is the account that added
-- it, and empty for every provider that speaks to an API with a key, which an
-- administrator chose to share with the others when they sealed it.
ALTER TABLE assistant_providers ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
