-- Record the thinking/reasoning level used for each proxied request.
-- Format: "<requested>" or "<requested>><applied>" (e.g. "high>max",
-- "low>disabled", "medium>unsupported"); empty when no effort was requested.
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS thinking_level VARCHAR(50) NOT NULL DEFAULT '';
