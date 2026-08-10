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
		fmt.Fprintln(os.Stderr, "nexler: usage: nexler init db [-dir <app-dir>]\n              nexler init kpass [-dir <app-dir>]\n              nexler init kgate [-dir <app-dir>]")
		os.Exit(1)
	}
	switch args[0] {
	case "db":
		runInitDB(args[1:])
	case "kpass":
		runInitKpass(args[1:])
	case "kgate":
		runInitKgate(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "nexler: unknown resource %q for init\n\nsupported: db, kpass, kgate\n", args[0])
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
		err = provisionSQL(ctx, dbType, dsn)
	case "mongo":
		err = provisionMongo(ctx, dsn)
	default:
		err = fmt.Errorf("unsupported core database type %q", dbType)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexler: init db: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Provisioned core data schema (core_config, core_error_log, core_kgate_channels) on the core database.")
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

func provisionSQL(ctx context.Context, dbType, dsn string) error {
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

func provisionMongo(ctx context.Context, uri string) error {
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
	configColl := client.Database("core").Collection("core_config")
	if _, err := configColl.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "key", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return err
	}

	// Not unique — core_error_log has no natural key, just a
	// non-unique index on occurred_at for a future "recent errors" query
	// to sort/filter on efficiently.
	errorLogColl := client.Database("core").Collection("core_error_log")
	if _, err := errorLogColl.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "occurred_at", Value: -1}},
	}); err != nil {
		return err
	}

	// Unique on channel — kgate.Subscribe's AddKgateChannel upserts on
	// this same field, so re-subscribing to an already-recorded channel
	// stays a no-op rather than creating a duplicate row.
	kgateChannelColl := client.Database("core").Collection("core_kgate_channels")
	_, err = kgateChannelColl.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "channel", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	return err
}
