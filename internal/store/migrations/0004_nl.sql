-- Global LLM settings: a single row (id = 1). API keys live in the secrets
-- vault (llm:anthropic, llm:openai_compat), never here.
CREATE TABLE llm_settings (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    provider      TEXT NOT NULL CHECK (provider IN ('none', 'anthropic', 'openai_compat')),
    model         TEXT NOT NULL,
    summary_model TEXT NOT NULL,
    base_url      TEXT NOT NULL DEFAULT '',
    effort        TEXT NOT NULL DEFAULT '',
    updated_at    TEXT NOT NULL
) STRICT;

-- Per-profile glossary: business terms the NL layer should honour, in the
-- order the user arranged them.
CREATE TABLE glossary (
    profile_id TEXT NOT NULL REFERENCES profiles (id) ON DELETE CASCADE,
    position   INTEGER NOT NULL,
    term       TEXT NOT NULL,
    definition TEXT NOT NULL,
    sql        TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (profile_id, position)
) STRICT;

-- Runs from the ask route are recorded with the question that produced them.
ALTER TABLE query_history ADD COLUMN source TEXT NOT NULL DEFAULT 'query' CHECK (source IN ('query', 'ask'));
ALTER TABLE query_history ADD COLUMN question TEXT;
