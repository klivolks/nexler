// This file implements `nexler init db [-dir <app-dir>]` — idempotently
// provisioning the schema for a generated app's "core" system data (see
// internal/scaffold/templates/core/config.go.tmpl) on its actual core
// database connection.
//
// This is deliberately separate from `nexler create app` and from the
// generated app's own startup (db.Connect()): a physical database can be
// shared by several scaffolded apps, so auto-provisioning on every boot
// would be redundant, and a common production setup runs the app itself
// with a low-privilege DB user that can't run DDL at all — schema setup
// belongs in its own explicit, higher-privilege step. Every statement here
// is idempotent (CREATE TABLE IF NOT EXISTS / CREATE OR REPLACE PROCEDURE /
// CREATE OR ALTER PROCEDURE / a unique index Mongo no-ops on if it already
// exists) — running this again, including against a database another app
// already provisioned, is always safe.
//
// This is the one place nexler itself (not just generated apps) needs a
// real database connection and real third-party driver dependencies — see
// go.mod.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// runInit dispatches `nexler init ...`.
func runInit(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "nexler: usage: nexler init db [-dir <app-dir>]\n              nexler init kpass [-dir <app-dir>]\n              nexler init kgate [-dir <app-dir>]\n              nexler init tenant [-dir <app-dir>]\n              nexler init docker [-dir <app-dir>]\n              nexler init ci [-dir <app-dir>] [-registry dockerhub|github]\n              nexler init kube [-dir <app-dir>] [-registry dockerhub|github] [-image <ref>] [-namespace <ns>] [-replicas <n>]")
		os.Exit(1)
	}
	switch args[0] {
	case "db":
		runInitDB(args[1:])
	case "kpass":
		runInitKpass(args[1:])
	case "kgate":
		runInitKgate(args[1:])
	case "tenant":
		runInitTenant(args[1:])
	case "docker":
		runInitDocker(args[1:])
	case "ci":
		runInitCI(args[1:])
	case "kube":
		runInitKube(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "nexler: unknown resource %q for init\n\nsupported: db, kpass, kgate, tenant, docker, ci, kube\n", args[0])
		os.Exit(1)
	}
}

func runInitDB(args []string) {
	fs := flag.NewFlagSet("init db", flag.ExitOnError)
	dir := fs.String("dir", ".", "path to the app directory (must contain .env); defaults to the current directory")
	fs.Parse(args)

	dbType, dsn, err := readCoreConnection(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexler: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch dbType {
	case "mysql", "postgres", "mssql":
		err = provisionSQL(ctx, dbType, dsn, *dir)
	case "mongo":
		err = provisionMongo(ctx, dsn, *dir)
	default:
		err = fmt.Errorf("unsupported core database type %q", dbType)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexler: init db: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Provisioned core data schema (core_config, core_error_log, core_kgate_channels, core_users, core_services, core_tasks, core_audit_log) on the core database.")
}

// tenantOrgEnabled reports whether appDir ran `nexler init tenant` — the
// signal to also provision tenant_orgs here, since that command itself
// never touches a live database connection (see tenant.go's own doc
// comment) and provisioning stays centralized in this one command.
func tenantOrgEnabled(appDir string) bool {
	_, err := os.Stat(filepath.Join(appDir, "tenant", "tenant.go"))
	return err == nil
}

// readCoreConnection reads appDir's .env for its core connection's
// type/DSN — the same {PREFIX}_DB_CORE_TYPE/_DSN convention
// scaffold.go's readCoreDBType reads (the env-var prefix varies per app,
// so this matches on the fixed suffix rather than needing to know it).
func readCoreConnection(appDir string) (dbType, dsn string, err error) {
	envPath := filepath.Join(appDir, ".env")
	raw, err := os.ReadFile(envPath)
	if err != nil {
		return "", "", fmt.Errorf("reading %s: %w (run this from inside a nexler app directory, or pass -dir)", envPath, err)
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		switch {
		case strings.HasSuffix(key, "_DB_CORE_TYPE"):
			dbType = val
		case strings.HasSuffix(key, "_DB_CORE_DSN"):
			dsn = val
		}
	}
	if dbType == "" {
		return "", "", fmt.Errorf("no _DB_CORE_TYPE found in %s — was this app scaffolded with -db?", envPath)
	}
	if dsn == "" {
		return "", "", fmt.Errorf("%s's core DSN is blank — fill in _DB_CORE_DSN before running init db", envPath)
	}
	return dbType, dsn, nil
}

// sqlDriverName maps a core-DB type to its database/sql driver name
// (mirrors db/sql.go.tmpl's sqlDriverName in the generated app template).
func sqlDriverName(dbType string) string {
	switch dbType {
	case "postgres":
		return "pgx"
	case "mssql":
		return "sqlserver"
	default:
		return dbType // "mysql"
	}
}

func provisionSQL(ctx context.Context, dbType, dsn, appDir string) error {
	conn, err := sql.Open(sqlDriverName(dbType), dsn)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.PingContext(ctx); err != nil {
		return fmt.Errorf("connecting: %w", err)
	}

	stmts := append(configStatements(dbType), errorLogStatements(dbType)...)
	stmts = append(stmts, kgateChannelStatements(dbType)...)
	stmts = append(stmts, usersStatements(dbType)...)
	stmts = append(stmts, servicesStatements(dbType)...)
	stmts = append(stmts, taskStatements(dbType)...)
	stmts = append(stmts, auditLogStatements(dbType)...)
	if tenantOrgEnabled(appDir) {
		stmts = append(stmts, tenantOrgStatements(dbType)...)
	}
	for _, stmt := range stmts {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("running provisioning statement: %w\n%s", err, stmt)
		}
	}
	return nil
}

// configStatements returns core_config's provisioning statements for
// dbType, run in order as separate Exec calls. Only the upsert needs a
// stored procedure — a native ON DUPLICATE KEY UPDATE / ON CONFLICT /
// MERGE, since each dialect's upsert syntax differs and nexler owns this
// schema, so writing it directly (rather than through the generic
// mysql/postgres/mssql packages' portable but upsert-less Insert/Update)
// is safe. A plain read doesn't need one — see configGetQuerySQL in
// internal/scaffold/scaffold.go, which the generated core package calls
// directly instead.
func configStatements(dbType string) []string {
	switch dbType {
	case "mysql":
		return []string{
			"CREATE TABLE IF NOT EXISTS core_config (" +
				"`key` VARCHAR(255) NOT NULL PRIMARY KEY, " +
				"value TEXT NOT NULL, " +
				"updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP)",
			"DROP PROCEDURE IF EXISTS core_config_upsert",
			"CREATE PROCEDURE core_config_upsert(IN p_key VARCHAR(255), IN p_value TEXT) " +
				"BEGIN " +
				"INSERT INTO core_config (`key`, value, updated_at) VALUES (p_key, p_value, NOW()) " +
				"ON DUPLICATE KEY UPDATE value = p_value, updated_at = NOW(); " +
				"END",
		}
	case "postgres":
		return []string{
			"CREATE TABLE IF NOT EXISTS core_config (" +
				"key TEXT PRIMARY KEY, " +
				"value TEXT NOT NULL, " +
				"updated_at TIMESTAMPTZ NOT NULL DEFAULT now())",
			"CREATE OR REPLACE PROCEDURE core_config_upsert(p_key TEXT, p_value TEXT) " +
				"LANGUAGE plpgsql AS $$ " +
				"BEGIN " +
				"INSERT INTO core_config (key, value, updated_at) VALUES (p_key, p_value, now()) " +
				"ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now(); " +
				"END; $$",
		}
	case "mssql":
		return []string{
			"IF NOT EXISTS (SELECT * FROM sys.tables WHERE name = 'core_config') " +
				"CREATE TABLE core_config (" +
				"[key] NVARCHAR(255) NOT NULL PRIMARY KEY, " +
				"value NVARCHAR(MAX) NOT NULL, " +
				"updated_at DATETIME2 NOT NULL DEFAULT SYSUTCDATETIME())",
			"CREATE OR ALTER PROCEDURE core_config_upsert " +
				"@p_key NVARCHAR(255), @p_value NVARCHAR(MAX) AS " +
				"BEGIN " +
				"MERGE core_config AS target " +
				"USING (SELECT @p_key AS [key]) AS src " +
				"ON target.[key] = src.[key] " +
				"WHEN MATCHED THEN UPDATE SET value = @p_value, updated_at = SYSUTCDATETIME() " +
				"WHEN NOT MATCHED THEN INSERT ([key], value, updated_at) VALUES (@p_key, @p_value, SYSUTCDATETIME()); " +
				"END",
		}
	default:
		return nil
	}
}

// errorLogStatements returns core_error_log's provisioning statements for
// dbType — a table plus a core_error_log_insert stored procedure. Unlike
// core_config's upsert, this insert has no per-dialect logic worth
// noting; it's still a procedure (rather than nexler's generated apps
// calling INSERT directly) purely for consistency with how every other
// core-owned table is written to.
func errorLogStatements(dbType string) []string {
	switch dbType {
	case "mysql":
		return []string{
			"CREATE TABLE IF NOT EXISTS core_error_log (" +
				"id INT AUTO_INCREMENT PRIMARY KEY, " +
				"occurred_at DATETIME NOT NULL, " +
				"message TEXT NOT NULL, " +
				"details TEXT, " +
				"route VARCHAR(255))",
			"DROP PROCEDURE IF EXISTS core_error_log_insert",
			"CREATE PROCEDURE core_error_log_insert(IN p_message TEXT, IN p_details TEXT, IN p_route VARCHAR(255)) " +
				"BEGIN " +
				"INSERT INTO core_error_log (occurred_at, message, details, route) VALUES (NOW(), p_message, p_details, p_route); " +
				"END",
		}
	case "postgres":
		return []string{
			"CREATE TABLE IF NOT EXISTS core_error_log (" +
				"id SERIAL PRIMARY KEY, " +
				"occurred_at TIMESTAMPTZ NOT NULL, " +
				"message TEXT NOT NULL, " +
				"details TEXT, " +
				"route TEXT)",
			"CREATE OR REPLACE PROCEDURE core_error_log_insert(p_message TEXT, p_details TEXT, p_route TEXT) " +
				"LANGUAGE plpgsql AS $$ " +
				"BEGIN " +
				"INSERT INTO core_error_log (occurred_at, message, details, route) VALUES (now(), p_message, p_details, p_route); " +
				"END; $$",
		}
	case "mssql":
		return []string{
			"IF NOT EXISTS (SELECT * FROM sys.tables WHERE name = 'core_error_log') " +
				"CREATE TABLE core_error_log (" +
				"id INT IDENTITY(1,1) PRIMARY KEY, " +
				"occurred_at DATETIME2 NOT NULL, " +
				"message NVARCHAR(MAX) NOT NULL, " +
				"details NVARCHAR(MAX), " +
				"route NVARCHAR(255))",
			"CREATE OR ALTER PROCEDURE core_error_log_insert " +
				"@p_message NVARCHAR(MAX), @p_details NVARCHAR(MAX), @p_route NVARCHAR(255) AS " +
				"BEGIN " +
				"INSERT INTO core_error_log (occurred_at, message, details, route) VALUES (SYSUTCDATETIME(), @p_message, @p_details, @p_route); " +
				"END",
		}
	default:
		return nil
	}
}

// kgateChannelStatements returns core_kgate_channels' provisioning
// statements for dbType — a table plus core_kgate_channel_add/_remove
// stored procedures (kgate.Subscribe/Unsubscribe). _add is an idempotent
// insert (no-op if the channel is already recorded, unlike core_config's
// upsert, since a channel row has no value to update — it's present or
// it isn't); _remove is a plain delete by channel.
func kgateChannelStatements(dbType string) []string {
	switch dbType {
	case "mysql":
		return []string{
			"CREATE TABLE IF NOT EXISTS core_kgate_channels (" +
				"channel VARCHAR(255) NOT NULL PRIMARY KEY, " +
				"created_at DATETIME NOT NULL)",
			"DROP PROCEDURE IF EXISTS core_kgate_channel_add",
			"CREATE PROCEDURE core_kgate_channel_add(IN p_channel VARCHAR(255)) " +
				"BEGIN " +
				"INSERT IGNORE INTO core_kgate_channels (channel, created_at) VALUES (p_channel, NOW()); " +
				"END",
			"DROP PROCEDURE IF EXISTS core_kgate_channel_remove",
			"CREATE PROCEDURE core_kgate_channel_remove(IN p_channel VARCHAR(255)) " +
				"BEGIN " +
				"DELETE FROM core_kgate_channels WHERE channel = p_channel; " +
				"END",
		}
	case "postgres":
		return []string{
			"CREATE TABLE IF NOT EXISTS core_kgate_channels (" +
				"channel TEXT PRIMARY KEY, " +
				"created_at TIMESTAMPTZ NOT NULL)",
			"CREATE OR REPLACE PROCEDURE core_kgate_channel_add(p_channel TEXT) " +
				"LANGUAGE plpgsql AS $$ " +
				"BEGIN " +
				"INSERT INTO core_kgate_channels (channel, created_at) VALUES (p_channel, now()) " +
				"ON CONFLICT (channel) DO NOTHING; " +
				"END; $$",
			"CREATE OR REPLACE PROCEDURE core_kgate_channel_remove(p_channel TEXT) " +
				"LANGUAGE plpgsql AS $$ " +
				"BEGIN " +
				"DELETE FROM core_kgate_channels WHERE channel = p_channel; " +
				"END; $$",
		}
	case "mssql":
		return []string{
			"IF NOT EXISTS (SELECT * FROM sys.tables WHERE name = 'core_kgate_channels') " +
				"CREATE TABLE core_kgate_channels (" +
				"[channel] NVARCHAR(255) NOT NULL PRIMARY KEY, " +
				"created_at DATETIME2 NOT NULL)",
			"CREATE OR ALTER PROCEDURE core_kgate_channel_add " +
				"@p_channel NVARCHAR(255) AS " +
				"BEGIN " +
				"IF NOT EXISTS (SELECT 1 FROM core_kgate_channels WHERE [channel] = @p_channel) " +
				"INSERT INTO core_kgate_channels ([channel], created_at) VALUES (@p_channel, SYSUTCDATETIME()); " +
				"END",
			"CREATE OR ALTER PROCEDURE core_kgate_channel_remove " +
				"@p_channel NVARCHAR(255) AS " +
				"BEGIN " +
				"DELETE FROM core_kgate_channels WHERE [channel] = @p_channel; " +
				"END",
		}
	default:
		return nil
	}
}

// usersStatements returns core_users' provisioning statements for dbType —
// a table plus core_user_upsert/core_user_set_status stored procedures.
// user_id is the same value as auth.Subject(r) / the JWT "sub" claim
// (see auth/context.go.tmpl's ContextWithService/Service doc comments and
// core/users.go.tmpl's package doc comment) — this table is a minimal
// local record (username/role/type/status), never a profile store; real
// user info lives wherever it already does, kpass-backed or not. org_id
// (for -multitenant apps' core.User.OrgId) is provisioned unconditionally
// for every -db app, same "zero-cost-when-unused" precedent as
// core_kgate_channels — a non-multitenant app's generated code simply
// never reads/writes it. The separate ALTER TABLE (alongside CREATE TABLE
// IF NOT EXISTS) is what lets an app that already ran `nexler init db`
// before org_id existed pick up the column on a re-run.
func usersStatements(dbType string) []string {
	switch dbType {
	case "mysql":
		return []string{
			"CREATE TABLE IF NOT EXISTS core_users (" +
				"user_id VARCHAR(255) NOT NULL PRIMARY KEY, " +
				"username VARCHAR(255) NOT NULL, " +
				"user_role VARCHAR(255) NOT NULL DEFAULT '', " +
				"user_type VARCHAR(255) NOT NULL DEFAULT '', " +
				"org_id VARCHAR(255) NOT NULL DEFAULT '', " +
				"status VARCHAR(32) NOT NULL DEFAULT 'active', " +
				"created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, " +
				"updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP)",
			"ALTER TABLE core_users ADD COLUMN IF NOT EXISTS org_id VARCHAR(255) NOT NULL DEFAULT ''",
			"DROP PROCEDURE IF EXISTS core_user_upsert",
			"CREATE PROCEDURE core_user_upsert(IN p_user_id VARCHAR(255), IN p_username VARCHAR(255), IN p_user_role VARCHAR(255), IN p_user_type VARCHAR(255), IN p_org_id VARCHAR(255)) " +
				"BEGIN " +
				"INSERT INTO core_users (user_id, username, user_role, user_type, org_id, created_at, updated_at) " +
				"VALUES (p_user_id, p_username, p_user_role, p_user_type, p_org_id, NOW(), NOW()) " +
				"ON DUPLICATE KEY UPDATE username = p_username, user_role = p_user_role, user_type = p_user_type, org_id = p_org_id, updated_at = NOW(); " +
				"END",
			"DROP PROCEDURE IF EXISTS core_user_set_status",
			"CREATE PROCEDURE core_user_set_status(IN p_user_id VARCHAR(255), IN p_status VARCHAR(32)) " +
				"BEGIN " +
				"UPDATE core_users SET status = p_status, updated_at = NOW() WHERE user_id = p_user_id; " +
				"END",
		}
	case "postgres":
		return []string{
			"CREATE TABLE IF NOT EXISTS core_users (" +
				"user_id TEXT PRIMARY KEY, " +
				"username TEXT NOT NULL, " +
				"user_role TEXT NOT NULL DEFAULT '', " +
				"user_type TEXT NOT NULL DEFAULT '', " +
				"org_id TEXT NOT NULL DEFAULT '', " +
				"status TEXT NOT NULL DEFAULT 'active', " +
				"created_at TIMESTAMPTZ NOT NULL DEFAULT now(), " +
				"updated_at TIMESTAMPTZ NOT NULL DEFAULT now())",
			"ALTER TABLE core_users ADD COLUMN IF NOT EXISTS org_id TEXT NOT NULL DEFAULT ''",
			"CREATE OR REPLACE PROCEDURE core_user_upsert(p_user_id TEXT, p_username TEXT, p_user_role TEXT, p_user_type TEXT, p_org_id TEXT) " +
				"LANGUAGE plpgsql AS $$ " +
				"BEGIN " +
				"INSERT INTO core_users (user_id, username, user_role, user_type, org_id, created_at, updated_at) " +
				"VALUES (p_user_id, p_username, p_user_role, p_user_type, p_org_id, now(), now()) " +
				"ON CONFLICT (user_id) DO UPDATE SET username = p_username, user_role = p_user_role, user_type = p_user_type, org_id = p_org_id, updated_at = now(); " +
				"END; $$",
			"CREATE OR REPLACE PROCEDURE core_user_set_status(p_user_id TEXT, p_status TEXT) " +
				"LANGUAGE plpgsql AS $$ " +
				"BEGIN " +
				"UPDATE core_users SET status = p_status, updated_at = now() WHERE user_id = p_user_id; " +
				"END; $$",
		}
	case "mssql":
		return []string{
			"IF NOT EXISTS (SELECT * FROM sys.tables WHERE name = 'core_users') " +
				"CREATE TABLE core_users (" +
				"[user_id] NVARCHAR(255) NOT NULL PRIMARY KEY, " +
				"username NVARCHAR(255) NOT NULL, " +
				"user_role NVARCHAR(255) NOT NULL DEFAULT '', " +
				"user_type NVARCHAR(255) NOT NULL DEFAULT '', " +
				"org_id NVARCHAR(255) NOT NULL DEFAULT '', " +
				"status NVARCHAR(32) NOT NULL DEFAULT 'active', " +
				"created_at DATETIME2 NOT NULL DEFAULT SYSUTCDATETIME(), " +
				"updated_at DATETIME2 NOT NULL DEFAULT SYSUTCDATETIME())",
			"IF NOT EXISTS (SELECT * FROM sys.columns WHERE object_id = OBJECT_ID('core_users') AND name = 'org_id') " +
				"ALTER TABLE core_users ADD org_id NVARCHAR(255) NOT NULL DEFAULT ''",
			"CREATE OR ALTER PROCEDURE core_user_upsert " +
				"@p_user_id NVARCHAR(255), @p_username NVARCHAR(255), @p_user_role NVARCHAR(255), @p_user_type NVARCHAR(255), @p_org_id NVARCHAR(255) AS " +
				"BEGIN " +
				"MERGE core_users AS target " +
				"USING (SELECT @p_user_id AS user_id) AS src " +
				"ON target.[user_id] = src.[user_id] " +
				"WHEN MATCHED THEN UPDATE SET username = @p_username, user_role = @p_user_role, user_type = @p_user_type, org_id = @p_org_id, updated_at = SYSUTCDATETIME() " +
				"WHEN NOT MATCHED THEN INSERT ([user_id], username, user_role, user_type, org_id, created_at, updated_at) " +
				"VALUES (@p_user_id, @p_username, @p_user_role, @p_user_type, @p_org_id, SYSUTCDATETIME(), SYSUTCDATETIME()); " +
				"END",
			"CREATE OR ALTER PROCEDURE core_user_set_status " +
				"@p_user_id NVARCHAR(255), @p_status NVARCHAR(32) AS " +
				"BEGIN " +
				"UPDATE core_users SET status = @p_status, updated_at = SYSUTCDATETIME() WHERE [user_id] = @p_user_id; " +
				"END",
		}
	default:
		return nil
	}
}

// servicesStatements returns core_services' provisioning statements for
// dbType — a table plus core_service_create/core_service_set_status
// stored procedures. key_hash (not the plaintext key, which is never
// stored — see core.CreateService) is UNIQUE inline in the CREATE TABLE
// itself, rather than a separate CREATE UNIQUE INDEX statement, since
// CREATE INDEX ... IF NOT EXISTS isn't portable across all three SQL
// dialects the way CREATE TABLE IF NOT EXISTS is.
func servicesStatements(dbType string) []string {
	switch dbType {
	case "mysql":
		return []string{
			"CREATE TABLE IF NOT EXISTS core_services (" +
				"name VARCHAR(255) NOT NULL PRIMARY KEY, " +
				"key_hash CHAR(64) NOT NULL UNIQUE, " +
				"status VARCHAR(32) NOT NULL DEFAULT 'active', " +
				"created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, " +
				"updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP)",
			"DROP PROCEDURE IF EXISTS core_service_create",
			"CREATE PROCEDURE core_service_create(IN p_name VARCHAR(255), IN p_key_hash CHAR(64)) " +
				"BEGIN " +
				"INSERT INTO core_services (name, key_hash, created_at, updated_at) VALUES (p_name, p_key_hash, NOW(), NOW()); " +
				"END",
			"DROP PROCEDURE IF EXISTS core_service_set_status",
			"CREATE PROCEDURE core_service_set_status(IN p_name VARCHAR(255), IN p_status VARCHAR(32)) " +
				"BEGIN " +
				"UPDATE core_services SET status = p_status, updated_at = NOW() WHERE name = p_name; " +
				"END",
		}
	case "postgres":
		return []string{
			"CREATE TABLE IF NOT EXISTS core_services (" +
				"name TEXT PRIMARY KEY, " +
				"key_hash CHAR(64) NOT NULL UNIQUE, " +
				"status TEXT NOT NULL DEFAULT 'active', " +
				"created_at TIMESTAMPTZ NOT NULL DEFAULT now(), " +
				"updated_at TIMESTAMPTZ NOT NULL DEFAULT now())",
			"CREATE OR REPLACE PROCEDURE core_service_create(p_name TEXT, p_key_hash CHAR(64)) " +
				"LANGUAGE plpgsql AS $$ " +
				"BEGIN " +
				"INSERT INTO core_services (name, key_hash, created_at, updated_at) VALUES (p_name, p_key_hash, now(), now()); " +
				"END; $$",
			"CREATE OR REPLACE PROCEDURE core_service_set_status(p_name TEXT, p_status TEXT) " +
				"LANGUAGE plpgsql AS $$ " +
				"BEGIN " +
				"UPDATE core_services SET status = p_status, updated_at = now() WHERE name = p_name; " +
				"END; $$",
		}
	case "mssql":
		return []string{
			"IF NOT EXISTS (SELECT * FROM sys.tables WHERE name = 'core_services') " +
				"CREATE TABLE core_services (" +
				"[name] NVARCHAR(255) NOT NULL PRIMARY KEY, " +
				"key_hash CHAR(64) NOT NULL UNIQUE, " +
				"status NVARCHAR(32) NOT NULL DEFAULT 'active', " +
				"created_at DATETIME2 NOT NULL DEFAULT SYSUTCDATETIME(), " +
				"updated_at DATETIME2 NOT NULL DEFAULT SYSUTCDATETIME())",
			"CREATE OR ALTER PROCEDURE core_service_create " +
				"@p_name NVARCHAR(255), @p_key_hash CHAR(64) AS " +
				"BEGIN " +
				"INSERT INTO core_services ([name], key_hash, created_at, updated_at) VALUES (@p_name, @p_key_hash, SYSUTCDATETIME(), SYSUTCDATETIME()); " +
				"END",
			"CREATE OR ALTER PROCEDURE core_service_set_status " +
				"@p_name NVARCHAR(255), @p_status NVARCHAR(32) AS " +
				"BEGIN " +
				"UPDATE core_services SET status = @p_status, updated_at = SYSUTCDATETIME() WHERE [name] = @p_name; " +
				"END",
		}
	default:
		return nil
	}
}

// taskStatements returns core_tasks' provisioning statements for dbType —
// a table plus a core_task_upsert stored procedure (core.SyncTasks
// upserts by name on every startup). path_params/req_schema/resp_schema
// are stored as JSON-encoded TEXT — SQL portability across mysql/
// postgres/mssql matters more here than native JSON column types.
func taskStatements(dbType string) []string {
	switch dbType {
	case "mysql":
		return []string{
			"CREATE TABLE IF NOT EXISTS core_tasks (" +
				"name VARCHAR(255) NOT NULL PRIMARY KEY, " +
				"require_auth BOOLEAN NOT NULL DEFAULT FALSE, " +
				"path_params TEXT, " +
				"req_schema TEXT, " +
				"resp_schema TEXT, " +
				"synced_at DATETIME NOT NULL)",
			"DROP PROCEDURE IF EXISTS core_task_upsert",
			"CREATE PROCEDURE core_task_upsert(IN p_name VARCHAR(255), IN p_require_auth BOOLEAN, IN p_path_params TEXT, IN p_req_schema TEXT, IN p_resp_schema TEXT) " +
				"BEGIN " +
				"INSERT INTO core_tasks (name, require_auth, path_params, req_schema, resp_schema, synced_at) " +
				"VALUES (p_name, p_require_auth, p_path_params, p_req_schema, p_resp_schema, NOW()) " +
				"ON DUPLICATE KEY UPDATE require_auth = p_require_auth, path_params = p_path_params, req_schema = p_req_schema, resp_schema = p_resp_schema, synced_at = NOW(); " +
				"END",
		}
	case "postgres":
		return []string{
			"CREATE TABLE IF NOT EXISTS core_tasks (" +
				"name TEXT PRIMARY KEY, " +
				"require_auth BOOLEAN NOT NULL DEFAULT FALSE, " +
				"path_params TEXT, " +
				"req_schema TEXT, " +
				"resp_schema TEXT, " +
				"synced_at TIMESTAMPTZ NOT NULL)",
			"CREATE OR REPLACE PROCEDURE core_task_upsert(p_name TEXT, p_require_auth BOOLEAN, p_path_params TEXT, p_req_schema TEXT, p_resp_schema TEXT) " +
				"LANGUAGE plpgsql AS $$ " +
				"BEGIN " +
				"INSERT INTO core_tasks (name, require_auth, path_params, req_schema, resp_schema, synced_at) " +
				"VALUES (p_name, p_require_auth, p_path_params, p_req_schema, p_resp_schema, now()) " +
				"ON CONFLICT (name) DO UPDATE SET require_auth = p_require_auth, path_params = p_path_params, req_schema = p_req_schema, resp_schema = p_resp_schema, synced_at = now(); " +
				"END; $$",
		}
	case "mssql":
		return []string{
			"IF NOT EXISTS (SELECT * FROM sys.tables WHERE name = 'core_tasks') " +
				"CREATE TABLE core_tasks (" +
				"[name] NVARCHAR(255) NOT NULL PRIMARY KEY, " +
				"require_auth BIT NOT NULL DEFAULT 0, " +
				"path_params NVARCHAR(MAX), " +
				"req_schema NVARCHAR(MAX), " +
				"resp_schema NVARCHAR(MAX), " +
				"synced_at DATETIME2 NOT NULL)",
			"CREATE OR ALTER PROCEDURE core_task_upsert " +
				"@p_name NVARCHAR(255), @p_require_auth BIT, @p_path_params NVARCHAR(MAX), @p_req_schema NVARCHAR(MAX), @p_resp_schema NVARCHAR(MAX) AS " +
				"BEGIN " +
				"MERGE core_tasks AS target " +
				"USING (SELECT @p_name AS [name]) AS src " +
				"ON target.[name] = src.[name] " +
				"WHEN MATCHED THEN UPDATE SET require_auth = @p_require_auth, path_params = @p_path_params, req_schema = @p_req_schema, resp_schema = @p_resp_schema, synced_at = SYSUTCDATETIME() " +
				"WHEN NOT MATCHED THEN INSERT ([name], require_auth, path_params, req_schema, resp_schema, synced_at) " +
				"VALUES (@p_name, @p_require_auth, @p_path_params, @p_req_schema, @p_resp_schema, SYSUTCDATETIME()); " +
				"END",
		}
	default:
		return nil
	}
}

// auditLogStatements returns core_audit_log's provisioning statements for
// dbType — a table plus a core_audit_log_insert stored procedure
// (middleware.RegisterTask's auditWrap calls core.LogAudit on every
// request it wraps). path_params/meta are stored as JSON-encoded TEXT,
// same portability reasoning as core_tasks above.
func auditLogStatements(dbType string) []string {
	switch dbType {
	case "mysql":
		return []string{
			"CREATE TABLE IF NOT EXISTS core_audit_log (" +
				"id INT AUTO_INCREMENT PRIMARY KEY, " +
				"occurred_at DATETIME NOT NULL, " +
				"actor VARCHAR(255) NOT NULL DEFAULT '', " +
				"actor_type VARCHAR(32) NOT NULL DEFAULT '', " +
				"action VARCHAR(255) NOT NULL, " +
				"path_params TEXT, " +
				"meta TEXT)",
			"DROP PROCEDURE IF EXISTS core_audit_log_insert",
			"CREATE PROCEDURE core_audit_log_insert(IN p_actor VARCHAR(255), IN p_actor_type VARCHAR(32), IN p_action VARCHAR(255), IN p_path_params TEXT, IN p_meta TEXT) " +
				"BEGIN " +
				"INSERT INTO core_audit_log (occurred_at, actor, actor_type, action, path_params, meta) VALUES (NOW(), p_actor, p_actor_type, p_action, p_path_params, p_meta); " +
				"END",
		}
	case "postgres":
		return []string{
			"CREATE TABLE IF NOT EXISTS core_audit_log (" +
				"id SERIAL PRIMARY KEY, " +
				"occurred_at TIMESTAMPTZ NOT NULL, " +
				"actor TEXT NOT NULL DEFAULT '', " +
				"actor_type TEXT NOT NULL DEFAULT '', " +
				"action TEXT NOT NULL, " +
				"path_params TEXT, " +
				"meta TEXT)",
			"CREATE OR REPLACE PROCEDURE core_audit_log_insert(p_actor TEXT, p_actor_type TEXT, p_action TEXT, p_path_params TEXT, p_meta TEXT) " +
				"LANGUAGE plpgsql AS $$ " +
				"BEGIN " +
				"INSERT INTO core_audit_log (occurred_at, actor, actor_type, action, path_params, meta) VALUES (now(), p_actor, p_actor_type, p_action, p_path_params, p_meta); " +
				"END; $$",
		}
	case "mssql":
		return []string{
			"IF NOT EXISTS (SELECT * FROM sys.tables WHERE name = 'core_audit_log') " +
				"CREATE TABLE core_audit_log (" +
				"id INT IDENTITY(1,1) PRIMARY KEY, " +
				"occurred_at DATETIME2 NOT NULL, " +
				"actor NVARCHAR(255) NOT NULL DEFAULT '', " +
				"actor_type NVARCHAR(32) NOT NULL DEFAULT '', " +
				"action NVARCHAR(255) NOT NULL, " +
				"path_params NVARCHAR(MAX), " +
				"meta NVARCHAR(MAX))",
			"CREATE OR ALTER PROCEDURE core_audit_log_insert " +
				"@p_actor NVARCHAR(255), @p_actor_type NVARCHAR(32), @p_action NVARCHAR(255), @p_path_params NVARCHAR(MAX), @p_meta NVARCHAR(MAX) AS " +
				"BEGIN " +
				"INSERT INTO core_audit_log (occurred_at, actor, actor_type, action, path_params, meta) VALUES (SYSUTCDATETIME(), @p_actor, @p_actor_type, @p_action, @p_path_params, @p_meta); " +
				"END",
		}
	default:
		return nil
	}
}

// tenantOrgStatements returns tenant_orgs' provisioning statements for
// dbType — a plain table, no stored procedures: List/Delete (the only
// operations `nexler init tenant` generates in this release) are plain
// SELECT/DELETE, and there's no upsert/create path in this schema yet to
// need one. id is a caller-assigned string (a business identifier or
// external system's own ID), not a nexler-generated auto-increment —
// tenant_orgs has no generated Create path of its own. settings holds
// free-form, provider/business-specific organization configuration as
// JSON-encoded TEXT.
func tenantOrgStatements(dbType string) []string {
	switch dbType {
	case "mysql":
		return []string{
			"CREATE TABLE IF NOT EXISTS tenant_orgs (" +
				"id VARCHAR(255) NOT NULL PRIMARY KEY, " +
				"name VARCHAR(255) NOT NULL, " +
				"settings TEXT, " +
				"status VARCHAR(32) NOT NULL DEFAULT 'active', " +
				"created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, " +
				"updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP)",
		}
	case "postgres":
		return []string{
			"CREATE TABLE IF NOT EXISTS tenant_orgs (" +
				"id TEXT PRIMARY KEY, " +
				"name TEXT NOT NULL, " +
				"settings TEXT, " +
				"status TEXT NOT NULL DEFAULT 'active', " +
				"created_at TIMESTAMPTZ NOT NULL DEFAULT now(), " +
				"updated_at TIMESTAMPTZ NOT NULL DEFAULT now())",
		}
	case "mssql":
		return []string{
			"IF NOT EXISTS (SELECT * FROM sys.tables WHERE name = 'tenant_orgs') " +
				"CREATE TABLE tenant_orgs (" +
				"[id] NVARCHAR(255) NOT NULL PRIMARY KEY, " +
				"name NVARCHAR(255) NOT NULL, " +
				"settings NVARCHAR(MAX), " +
				"status NVARCHAR(32) NOT NULL DEFAULT 'active', " +
				"created_at DATETIME2 NOT NULL DEFAULT SYSUTCDATETIME(), " +
				"updated_at DATETIME2 NOT NULL DEFAULT SYSUTCDATETIME())",
		}
	default:
		return nil
	}
}

func provisionMongo(ctx context.Context, uri, appDir string) error {
	dbName, err := mongoDatabaseName(uri)
	if err != nil {
		return err
	}

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return err
	}
	defer client.Disconnect(context.Background())
	if err := client.Ping(ctx, nil); err != nil {
		return fmt.Errorf("connecting: %w", err)
	}

	// Indexes().CreateOne implicitly creates the collection if it doesn't
	// exist yet, and no-ops (no error) if this exact index already exists
	// — no separate CreateCollection call, and no IF NOT EXISTS needed.
	configColl := client.Database(dbName).Collection("core_config")
	if _, err := configColl.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "key", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return err
	}

	// Not unique — core_error_log has no natural key, just a
	// non-unique index on occurred_at for a future "recent errors" query
	// to sort/filter on efficiently.
	errorLogColl := client.Database(dbName).Collection("core_error_log")
	if _, err := errorLogColl.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "occurred_at", Value: -1}},
	}); err != nil {
		return err
	}

	// Unique on channel — kgate.Subscribe's AddKgateChannel upserts on
	// this same field, so re-subscribing to an already-recorded channel
	// stays a no-op rather than creating a duplicate row.
	kgateChannelColl := client.Database(dbName).Collection("core_kgate_channels")
	if _, err := kgateChannelColl.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "channel", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return err
	}

	// Unique on user_id — the same value as auth.Subject(r) / the JWT
	// "sub" claim (see core/users.go.tmpl) — core.CreateUser upserts on
	// this field.
	usersColl := client.Database(dbName).Collection("core_users")
	if _, err := usersColl.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "user_id", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return err
	}

	// Two unique indexes: name (a service's stable, human-assigned
	// identifier) and key_hash (what VerifyServiceKey looks up by on
	// every service-authenticated request) — never the plaintext key
	// itself, which core.CreateService never stores.
	servicesColl := client.Database(dbName).Collection("core_services")
	if _, err := servicesColl.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "name", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return err
	}
	if _, err := servicesColl.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "key_hash", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return err
	}

	// Unique on name — core.SyncTasks upserts on this field.
	tasksColl := client.Database(dbName).Collection("core_tasks")
	if _, err := tasksColl.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "name", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return err
	}

	// Not unique — core_audit_log has no natural key, just a non-unique
	// index on occurred_at for a future "recent activity" query to
	// sort/filter on efficiently, same reasoning as core_error_log above.
	auditLogColl := client.Database(dbName).Collection("core_audit_log")
	if _, err := auditLogColl.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "occurred_at", Value: -1}},
	}); err != nil {
		return err
	}

	// tenant_orgs needs no explicit provisioning even when `nexler init
	// tenant` has generated tenant/tenant.go for this app: List/Delete
	// only ever address it by Mongo's own default _id index, and Mongo
	// creates a collection lazily on first insert — nothing to provision
	// up front, unlike every SQL dialect's own CREATE TABLE below.
	return nil
}

// mongoDatabaseName extracts the database name from a Mongo connection
// string's path segment — e.g. "mydb" from
// "mongodb://host:27017/mydb?retryWrites=true" — mirroring
// db/mongo.go.tmpl's own mongoDatabaseName in the generated app template
// (same reasoning: the Mongo driver has no "default database from the
// URI" concept, so nexler must parse it out itself rather than assume a
// name). Without this, provisioning always wrote to a database literally
// named "core", ignoring whatever database the DSN actually pointed at.
func mongoDatabaseName(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("parsing core DSN: %w", err)
	}
	dbName := strings.TrimPrefix(u.Path, "/")
	if dbName == "" {
		return "", fmt.Errorf("core DSN has no database name in its path (expected mongodb://.../<dbname>)")
	}
	return dbName, nil
}
