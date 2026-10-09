-- The whole of the last preflight, as the JSON the check returned, beside the
-- one sentence last_check_error already keeps.
--
-- A card has room for the sentence, and the sentence is a finding's title:
-- "node pve1 has no bridge called vmbr0". What an operator needs to act on it is
-- the detail ("It offers vmbr0 and vmbr1") and the fix, and those lived only in
-- the browser tab that pressed Check, so they were gone on the next reload.
-- Opaque to the store, which only keeps it; the controller writes and reads it.
ALTER TABLE providers ADD COLUMN last_check_report TEXT NOT NULL DEFAULT '';
