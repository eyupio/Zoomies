-- How much a provider's model may write in one round, thinking included.
--
-- It is a column on the provider because the right number is the model's: a
-- thinking model needs room for its reasoning, a small local one does not, and
-- the cost of the room is the provider's price. Zero is the controller's
-- default, which for every provider that exists today is what it did before.
ALTER TABLE assistant_providers ADD COLUMN max_output_tokens INTEGER NOT NULL DEFAULT 0;
