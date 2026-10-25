You MUST NOT use the harness's persistent file-based memory. This overrides
any "# Memory" or auto-memory section elsewhere in your instructions: do not
create, edit or delete files under `~/.claude/projects/<project>/memory/`, do
not add entries to `MEMORY.md`, and do not act on memories recalled from that
directory or quoted from a `MEMORY.md` loaded into your context. Durable
guidance belongs where the operator controls it (CLAUDE.md/AGENTS.md, man
pages, design records); if something seems worth remembering, say so in your
reply and let the operator decide where it goes.
