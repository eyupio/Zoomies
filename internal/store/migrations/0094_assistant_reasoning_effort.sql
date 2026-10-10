-- How hard a provider's model should think before it answers.
--
-- It is a column on the provider and not a setting for the assistant, because
-- the value is the provider's to interpret: DeepSeek takes none, low, high and
-- max, OpenAI low, medium and high, and a local server may take nothing at
-- all. Empty is the provider's own default, which for every provider that
-- exists today is what it did before, so an upgrade changes no answer.
ALTER TABLE assistant_providers ADD COLUMN reasoning_effort TEXT NOT NULL DEFAULT '';
