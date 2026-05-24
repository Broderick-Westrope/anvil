# Phase 4: Manual Data Migration

> **Status:** DRAFT
>
> **Scope:** One-time manual migration of existing data on the developer's machine. Not committed to the codebase. Populates the new tree columns and drops the old summary columns.
>
> **Depends on:** Phase 2 merged (new columns exist, code uses them, fallback handles un-migrated data)
>
> **Timing:** Run after Phase 2 is merged. Can be done before or after Phase 3 — Phase 2's fallback path keeps existing sessions working in the meantime.

## Prerequisites

- Phase 2 code is merged and running (goose has auto-added the new columns)
- Know the exact DB path (likely `~/.config/anvil/anvil.db` or similar)
- Back up the database: `cp <db_path> <db_path>.bak`

## Task 9: Manual migration

**Steps:**

1. [ ] Write a SQL migration script (temporary, not in repo) that:
   - Chains existing messages linearly per session:
     ```sql
     WITH ordered AS (
       SELECT id, session_id,
              LAG(id) OVER (PARTITION BY session_id ORDER BY created_at ASC, rowid ASC) as prev_id
       FROM messages
     )
     UPDATE messages SET parent_message_id = (
       SELECT prev_id FROM ordered WHERE ordered.id = messages.id
     );
     ```
   - Sets `leaf_message_id` per session:
     ```sql
     UPDATE sessions SET leaf_message_id = (
       SELECT id FROM messages
       WHERE messages.session_id = sessions.id
       ORDER BY created_at DESC, rowid DESC
       LIMIT 1
     );
     ```
   - Converts `summary_message_id` references to compaction messages:
     ```sql
     UPDATE messages SET message_type = 'compaction'
     WHERE id IN (SELECT summary_message_id FROM sessions WHERE summary_message_id IS NOT NULL);
     ```
     Note: The `firstKeptEntryId` population requires finding the next message after the summary and constructing `CompactionContent` JSON in the `parts` column. Write a small Go script or manually construct the JSON for any affected sessions.
   - Drops old columns:
     ```sql
     ALTER TABLE sessions DROP COLUMN summary_message_id;
     ALTER TABLE messages DROP COLUMN is_summary_message;
     ```

2. [ ] Execute the migration:
   - `sqlite3 <db_path> < migrate_session_trees.sql`
   - Run the Go script for compaction metadata if needed
   - Verify:
     ```bash
     sqlite3 <db_path> "SELECT count(*) FROM messages WHERE parent_message_id IS NOT NULL;"
     # Should be > 0
     sqlite3 <db_path> "SELECT count(*) FROM sessions WHERE leaf_message_id IS NOT NULL;"
     # Should match sessions with messages
     sqlite3 <db_path> "PRAGMA table_info(sessions);"
     # No summary_message_id column
     sqlite3 <db_path> "PRAGMA table_info(messages);"
     # No is_summary_message column, has parent_message_id, message_type
     ```

3. [ ] After dropping old columns, update the sqlc queries to remove `summary_message_id` and `is_summary_message` from SELECT lists, then `sqlc generate` and `go build .` to confirm. This cleanup can be a small follow-up commit.

4. [ ] Launch the application and verify:
   - Existing sessions load via tree walk (not fallback)
   - Chat history displays properly
   - New messages advance the leaf pointer
   - `/tree` shows the linear history as a single branch (if Phase 3 is merged)
