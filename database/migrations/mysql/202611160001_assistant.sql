-- migrate:up
CREATE TABLE assistant_conversation (
 id VARCHAR(36) PRIMARY KEY, title VARCHAR(200) NOT NULL,
 active_run_id VARCHAR(36) NULL, created_at VARCHAR(40) NOT NULL, updated_at VARCHAR(40) NOT NULL
);
CREATE TABLE assistant_run (
 id VARCHAR(36) PRIMARY KEY, conversation_id VARCHAR(36) NOT NULL,
 request_id VARCHAR(80) NOT NULL, input_hash VARCHAR(64) NOT NULL, status VARCHAR(24) NOT NULL,
 model_service_id VARCHAR(36) NOT NULL, model_revision BIGINT NOT NULL,
 next_sequence BIGINT NOT NULL DEFAULT 0, error LONGTEXT NOT NULL,
 created_at VARCHAR(40) NOT NULL, updated_at VARCHAR(40) NOT NULL,
 UNIQUE(conversation_id, request_id),
 FOREIGN KEY(conversation_id) REFERENCES assistant_conversation(id) ON DELETE CASCADE
);
CREATE INDEX assistant_run_conversation ON assistant_run(conversation_id,created_at);
CREATE TABLE assistant_message (
 id VARCHAR(36) PRIMARY KEY, conversation_id VARCHAR(36) NOT NULL, run_id VARCHAR(36) NOT NULL,
 role VARCHAR(20) NOT NULL, content LONGTEXT NOT NULL, wire LONGTEXT NULL, created_at VARCHAR(40) NOT NULL,
 FOREIGN KEY(conversation_id) REFERENCES assistant_conversation(id) ON DELETE CASCADE,
 FOREIGN KEY(run_id) REFERENCES assistant_run(id) ON DELETE CASCADE
);
CREATE INDEX assistant_message_conversation ON assistant_message(conversation_id,created_at,id);
CREATE TABLE assistant_event (
 run_id VARCHAR(36) NOT NULL, sequence BIGINT NOT NULL, kind VARCHAR(40) NOT NULL,
 data LONGTEXT NOT NULL, created_at VARCHAR(40) NOT NULL, PRIMARY KEY(run_id,sequence),
 FOREIGN KEY(run_id) REFERENCES assistant_run(id) ON DELETE CASCADE
);
CREATE TABLE assistant_operation (
 id VARCHAR(36) PRIMARY KEY, run_id VARCHAR(36) NOT NULL, tool_name VARCHAR(100) NOT NULL,
 state VARCHAR(24) NOT NULL, arguments LONGTEXT NOT NULL, preview LONGTEXT NOT NULL,
 revision VARCHAR(64) NOT NULL, result LONGTEXT NOT NULL, error LONGTEXT NOT NULL,
 decision_id VARCHAR(80) NULL, decision VARCHAR(12) NULL, created_at VARCHAR(40) NOT NULL, updated_at VARCHAR(40) NOT NULL,
 FOREIGN KEY(run_id) REFERENCES assistant_run(id) ON DELETE CASCADE
);
CREATE INDEX assistant_operation_run ON assistant_operation(run_id,created_at,id);
CREATE TABLE assistant_checkpoint (
 id VARCHAR(200) PRIMARY KEY, run_id VARCHAR(36) NOT NULL, data LONGBLOB NOT NULL,
 FOREIGN KEY(run_id) REFERENCES assistant_run(id) ON DELETE CASCADE
);
-- migrate:down
DROP TABLE assistant_checkpoint;
DROP TABLE assistant_operation;
DROP TABLE assistant_event;
DROP TABLE assistant_message;
DROP TABLE assistant_run;
DROP TABLE assistant_conversation;
