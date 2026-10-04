-- Whether the repository's installed workflow is one an earlier release wrote.
-- Verification already tells that apart from somebody's edit, so it records the
-- answer here rather than the page having to ask GitHub on every load. Zero is
-- "current or not yet checked", which is what every existing row keeps until
-- its next verification.
ALTER TABLE ai_context_repositories ADD COLUMN workflow_outdated INTEGER NOT NULL DEFAULT 0;
