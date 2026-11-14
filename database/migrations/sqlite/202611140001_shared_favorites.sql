-- migrate:up
CREATE TABLE nas_favorites_state (
 id INTEGER PRIMARY KEY,
 revision BIGINT NOT NULL DEFAULT 0
);
INSERT INTO nas_favorites_state(id, revision) VALUES (1, 0);
CREATE TABLE nas_favorite (
 song_id VARCHAR(160) PRIMARY KEY,
 added_at VARCHAR(40) NOT NULL,
 song_json TEXT NOT NULL
);

-- migrate:down
DROP TABLE nas_favorite;
DROP TABLE nas_favorites_state;
