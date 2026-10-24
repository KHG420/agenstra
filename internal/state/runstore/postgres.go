package runstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Store is the common run/checkpoint store; SQLiteStore remains a source-compatible alias surface.
type Store = SQLiteStore

// NewPostgresStore opens a dedicated schema in an existing PostgreSQL database.
// It never creates a database or role. The caller owns Close; Initialize creates run tables.
func NewPostgresStore(dsn, schema string) (*Store, error) {
	config := agentcontract.DatabaseConfig{Driver: "postgres", DSNEnv: "DSN", Schema: schema}
	if err := config.Validate(false); err != nil {
		return nil, err
	}
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("PostgreSQL DSN is required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL DSN")
	}
	cfg.RuntimeParams["search_path"] = schema
	cfg.RuntimeParams["application_name"] = "agenstra"
	cfg.RuntimeParams["lock_timeout"] = "30000"
	cfg.RuntimeParams["statement_timeout"] = "30000"
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 10 * time.Second
	}
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(1)
	s := &Store{Path: schema, DB: db, Clock: UnixNow, Postgres: true}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		return nil, errors.Join(errors.New("PostgreSQL unavailable (check connection and credentials)"), db.Close())
	}
	err = s.Transaction(func(tx *sql.Tx) error {
		_, e := tx.Exec("CREATE SCHEMA IF NOT EXISTS " + pgx.Identifier{schema}.Sanitize())
		return e
	})
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return s, nil
}

// SchemaSQL translates only this package's trusted static DDL to PostgreSQL.
// It is not a query rewriter; shared queries use numbered parameters on both engines.
func (s *SQLiteStore) SchemaSQL(ddl string) string {
	if !s.Postgres {
		return ddl
	}
	return strings.NewReplacer("INTEGER PRIMARY KEY AUTOINCREMENT", "BIGSERIAL PRIMARY KEY", " REAL", " DOUBLE PRECISION").Replace(ddl)
}

func (s *SQLiteStore) initializePostgres() error {
	return s.Transaction(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS store_meta (id INTEGER PRIMARY KEY CHECK(id=1), version INTEGER NOT NULL);
		 INSERT INTO store_meta(id,version) VALUES(1,1) ON CONFLICT(id) DO NOTHING`); err != nil {
			return err
		}
		var version int
		if err := tx.QueryRow("SELECT version FROM store_meta WHERE id=1").Scan(&version); err != nil {
			return err
		}
		if version != 1 {
			return fmt.Errorf("unsupported PostgreSQL store schema version %d", version)
		}
		// rowid preserves insertion order used by invocation history; explicit
		// INSERT column lists ensure it is never supplied by the caller.
		ddl := strings.Replace(runSchema, "CREATE TABLE IF NOT EXISTS invocations (", "CREATE TABLE IF NOT EXISTS invocations (rowid BIGSERIAL UNIQUE,", 1)
		ddl = strings.Replace(ddl, "event_json TEXT", "event_json JSONB", 1)
		if _, err := tx.Exec(s.SchemaSQL(ddl)); err != nil {
			return err
		}
		if err := s.initializeScheduleTables(tx); err != nil {
			return err
		}
		return s.initializeMemoryTables(tx)
	})
}
