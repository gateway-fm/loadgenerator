package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/gateway-fm/loadgenerator/pkg/types"
)

// unmarshalJSON unmarshals JSON and logs any errors without failing.
// This is used for non-critical JSON fields where we want to gracefully
// handle corruption without failing the entire query.
func unmarshalJSON(data string, v any, field string, runID string) {
	if err := json.Unmarshal([]byte(data), v); err != nil {
		slog.Warn("failed to unmarshal JSON field",
			"field", field,
			"runID", runID,
			"error", err.Error(),
			"dataLen", len(data))
	}
}

// SQLiteStorage implements Storage using SQLite.
type SQLiteStorage struct {
	db *sql.DB
}

// NewSQLiteStorage creates a new SQLite storage instance.
func NewSQLiteStorage(dbPath string) (*SQLiteStorage, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	// Open database with WAL mode for better concurrent performance
	dsn := fmt.Sprintf("%s?_journal=WAL&_sync=NORMAL&_cache_size=10000&_foreign_keys=ON", dbPath)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Test connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	s := &SQLiteStorage{db: db}

	// Run migrations
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return s, nil
}

// migrate runs database migrations.
func (s *SQLiteStorage) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS test_runs (
		id TEXT PRIMARY KEY,
		started_at DATETIME NOT NULL,
		completed_at DATETIME,
		pattern TEXT NOT NULL,
		transaction_type TEXT NOT NULL,
		duration_ms INTEGER NOT NULL,
		tx_sent INTEGER DEFAULT 0,
		tx_confirmed INTEGER DEFAULT 0,
		tx_failed INTEGER DEFAULT 0,
		tx_discarded INTEGER DEFAULT 0,
		average_tps REAL DEFAULT 0,
		peak_tps INTEGER DEFAULT 0,
		latency_stats TEXT,
		preconf_latency_stats TEXT,
		config TEXT NOT NULL,
		status TEXT DEFAULT 'running',
		error_message TEXT,
		tx_logging_enabled INTEGER DEFAULT 1,
		execution_layer TEXT DEFAULT 'reth',
		block_count INTEGER DEFAULT 0,
		total_gas_used INTEGER DEFAULT 0,
		avg_fill_rate REAL DEFAULT 0,
		peak_mgas_per_sec REAL DEFAULT 0,
		avg_mgas_per_sec REAL DEFAULT 0,
		custom_name TEXT,
		is_favorite INTEGER DEFAULT 0
	);

	CREATE INDEX IF NOT EXISTS idx_test_runs_started ON test_runs(started_at DESC);

	CREATE TABLE IF NOT EXISTS time_series (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		test_run_id TEXT NOT NULL,
		timestamp_ms INTEGER NOT NULL,
		tx_sent INTEGER,
		tx_confirmed INTEGER,
		tx_failed INTEGER,
		current_tps REAL,
		target_tps INTEGER,
		pending_count INTEGER,
		gas_used INTEGER DEFAULT 0,
		gas_limit INTEGER DEFAULT 0,
		block_count INTEGER DEFAULT 0,
		mgas_per_sec REAL DEFAULT 0,
		fill_rate REAL DEFAULT 0,
		FOREIGN KEY (test_run_id) REFERENCES test_runs(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_time_series_run ON time_series(test_run_id);

	CREATE TABLE IF NOT EXISTS tx_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		test_run_id TEXT NOT NULL,
		tx_hash TEXT NOT NULL,
		sent_at_ms INTEGER NOT NULL,
		confirmed_at_ms INTEGER,
		preconf_at_ms INTEGER,
		confirm_latency_ms INTEGER,
		preconf_latency_ms INTEGER,
		status TEXT DEFAULT 'pending',
		error_reason TEXT,
		from_account INTEGER,
		nonce INTEGER,
		gas_tip_gwei REAL,
		FOREIGN KEY (test_run_id) REFERENCES test_runs(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_tx_logs_run ON tx_logs(test_run_id);
	CREATE INDEX IF NOT EXISTS idx_tx_logs_hash ON tx_logs(tx_hash);
	`

	if _, err := s.db.Exec(schema); err != nil {
		return err
	}

	// Run migration to add new columns if they don't exist
	// Uses a helper that checks if column exists before adding
	migrations := []struct {
		table  string
		column string
		ddl    string
	}{
		{"test_runs", "block_count", "ALTER TABLE test_runs ADD COLUMN block_count INTEGER DEFAULT 0"},
		{"test_runs", "total_gas_used", "ALTER TABLE test_runs ADD COLUMN total_gas_used INTEGER DEFAULT 0"},
		{"test_runs", "avg_fill_rate", "ALTER TABLE test_runs ADD COLUMN avg_fill_rate REAL DEFAULT 0"},
		{"test_runs", "peak_mgas_per_sec", "ALTER TABLE test_runs ADD COLUMN peak_mgas_per_sec REAL DEFAULT 0"},
		{"test_runs", "avg_mgas_per_sec", "ALTER TABLE test_runs ADD COLUMN avg_mgas_per_sec REAL DEFAULT 0"},
		{"test_runs", "execution_layer", "ALTER TABLE test_runs ADD COLUMN execution_layer TEXT DEFAULT 'reth'"},
		{"time_series", "gas_used", "ALTER TABLE time_series ADD COLUMN gas_used INTEGER DEFAULT 0"},
		{"time_series", "gas_limit", "ALTER TABLE time_series ADD COLUMN gas_limit INTEGER DEFAULT 0"},
		{"time_series", "block_count", "ALTER TABLE time_series ADD COLUMN block_count INTEGER DEFAULT 0"},
		{"time_series", "mgas_per_sec", "ALTER TABLE time_series ADD COLUMN mgas_per_sec REAL DEFAULT 0"},
		{"time_series", "fill_rate", "ALTER TABLE time_series ADD COLUMN fill_rate REAL DEFAULT 0"},
		// Test naming and favorites
		{"test_runs", "custom_name", "ALTER TABLE test_runs ADD COLUMN custom_name TEXT"},
		{"test_runs", "is_favorite", "ALTER TABLE test_runs ADD COLUMN is_favorite INTEGER DEFAULT 0"},
		// Discarded transactions (pending at test end)
		{"test_runs", "tx_discarded", "ALTER TABLE test_runs ADD COLUMN tx_discarded INTEGER DEFAULT 0"},
		// Realistic test specific metrics
		{"test_runs", "tip_histogram", "ALTER TABLE test_runs ADD COLUMN tip_histogram TEXT"},
		{"test_runs", "tx_type_metrics", "ALTER TABLE test_runs ADD COLUMN tx_type_metrics TEXT"},
		{"test_runs", "pending_latency_stats", "ALTER TABLE test_runs ADD COLUMN pending_latency_stats TEXT"},
		{"test_runs", "accounts_active", "ALTER TABLE test_runs ADD COLUMN accounts_active INTEGER DEFAULT 0"},
		{"test_runs", "accounts_funded", "ALTER TABLE test_runs ADD COLUMN accounts_funded INTEGER DEFAULT 0"},
		// On-chain verification metrics
		{"test_runs", "on_chain_first_block", "ALTER TABLE test_runs ADD COLUMN on_chain_first_block INTEGER DEFAULT 0"},
		{"test_runs", "on_chain_last_block", "ALTER TABLE test_runs ADD COLUMN on_chain_last_block INTEGER DEFAULT 0"},
		{"test_runs", "on_chain_tx_count", "ALTER TABLE test_runs ADD COLUMN on_chain_tx_count INTEGER DEFAULT 0"},
		{"test_runs", "on_chain_gas_used", "ALTER TABLE test_runs ADD COLUMN on_chain_gas_used INTEGER DEFAULT 0"},
		{"test_runs", "on_chain_mgas_per_sec", "ALTER TABLE test_runs ADD COLUMN on_chain_mgas_per_sec REAL DEFAULT 0"},
		{"test_runs", "on_chain_tps", "ALTER TABLE test_runs ADD COLUMN on_chain_tps REAL DEFAULT 0"},
		{"test_runs", "on_chain_duration_secs", "ALTER TABLE test_runs ADD COLUMN on_chain_duration_secs REAL DEFAULT 0"},
		// Environment and verification data (JSON)
		{"test_runs", "environment", "ALTER TABLE test_runs ADD COLUMN environment TEXT"},
		{"test_runs", "verification", "ALTER TABLE test_runs ADD COLUMN verification TEXT"},
		// Deployed contracts and test accounts (JSON)
		{"test_runs", "deployed_contracts", "ALTER TABLE test_runs ADD COLUMN deployed_contracts TEXT"},
		{"test_runs", "test_accounts", "ALTER TABLE test_runs ADD COLUMN test_accounts TEXT"},
	}

	for _, m := range migrations {
		if !s.columnExists(m.table, m.column) {
			if _, err := s.db.Exec(m.ddl); err != nil {
				// Log but don't fail - migration might have already been applied
				fmt.Fprintf(os.Stderr, "warning: migration failed for %s.%s: %v\n", m.table, m.column, err)
			}
		}
	}

	return nil
}

// columnExists checks if a column exists in a table.
// Note: table and column names are validated to prevent SQL injection.
// SQLite identifiers only allow alphanumeric chars and underscore.
func (s *SQLiteStorage) columnExists(table, column string) bool {
	// Validate identifiers to prevent SQL injection
	if !isValidIdentifier(table) || !isValidIdentifier(column) {
		return false
	}
	query := fmt.Sprintf("SELECT COUNT(*) FROM pragma_table_info('%s') WHERE name = '%s'", table, column)
	var count int
	if err := s.db.QueryRow(query).Scan(&count); err != nil {
		return false
	}
	return count > 0
}

// isValidIdentifier checks if a string is a valid SQLite identifier.
// Only allows alphanumeric characters and underscore.
func isValidIdentifier(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

// Close closes the database connection.
func (s *SQLiteStorage) Close() error {
	return s.db.Close()
}

// CreateTestRun creates a new test run record.
func (s *SQLiteStorage) CreateTestRun(ctx context.Context, run *TestRun) error {
	configJSON, err := json.Marshal(run.Config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Default to "reth" if not specified
	executionLayer := run.ExecutionLayer
	if executionLayer == "" {
		executionLayer = "reth"
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO test_runs (id, started_at, pattern, transaction_type, duration_ms, config, status, tx_logging_enabled, execution_layer)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, run.ID, run.StartedAt, run.Pattern, run.TransactionType, run.DurationMs, string(configJSON), run.Status, run.TxLoggingEnabled, executionLayer)

	return err
}

// UpdateTestRun updates an existing test run.
func (s *SQLiteStorage) UpdateTestRun(ctx context.Context, run *TestRun) error {
	latencyJSON, _ := json.Marshal(run.LatencyStats)
	preconfJSON, _ := json.Marshal(run.PreconfLatency)

	_, err := s.db.ExecContext(ctx, `
		UPDATE test_runs SET
			tx_sent = ?,
			tx_confirmed = ?,
			tx_failed = ?,
			average_tps = ?,
			peak_tps = ?,
			latency_stats = ?,
			preconf_latency_stats = ?,
			status = ?,
			error_message = ?
		WHERE id = ?
	`, run.TxSent, run.TxConfirmed, run.TxFailed, run.AverageTPS, run.PeakTPS,
		string(latencyJSON), string(preconfJSON), run.Status, run.ErrorMessage, run.ID)

	return err
}

// CompleteTestRun marks a test run as completed with final statistics.
func (s *SQLiteStorage) CompleteTestRun(ctx context.Context, id string, run *TestRun) error {
	latencyJSON, _ := json.Marshal(run.LatencyStats)
	preconfJSON, _ := json.Marshal(run.PreconfLatency)
	pendingLatencyJSON, _ := json.Marshal(run.PendingLatency)
	tipHistogramJSON, _ := json.Marshal(run.TipHistogram)
	txTypeMetricsJSON, _ := json.Marshal(run.TxTypeMetrics)
	environmentJSON, _ := json.Marshal(run.Environment)
	verificationJSON, _ := json.Marshal(run.Verification)
	deployedContractsJSON, _ := json.Marshal(run.DeployedContracts)
	testAccountsJSON, _ := json.Marshal(run.TestAccounts)

	now := time.Now()
	_, err := s.db.ExecContext(ctx, `
		UPDATE test_runs SET
			completed_at = ?,
			tx_sent = ?,
			tx_confirmed = ?,
			tx_failed = ?,
			tx_discarded = ?,
			average_tps = ?,
			peak_tps = ?,
			latency_stats = ?,
			preconf_latency_stats = ?,
			pending_latency_stats = ?,
			status = ?,
			error_message = ?,
			block_count = ?,
			total_gas_used = ?,
			avg_fill_rate = ?,
			peak_mgas_per_sec = ?,
			avg_mgas_per_sec = ?,
			tip_histogram = ?,
			tx_type_metrics = ?,
			accounts_active = ?,
			accounts_funded = ?,
			on_chain_first_block = ?,
			on_chain_last_block = ?,
			on_chain_tx_count = ?,
			on_chain_gas_used = ?,
			on_chain_mgas_per_sec = ?,
			on_chain_tps = ?,
			on_chain_duration_secs = ?,
			environment = ?,
			verification = ?,
			deployed_contracts = ?,
			test_accounts = ?
		WHERE id = ?
	`, now, run.TxSent, run.TxConfirmed, run.TxFailed, run.TxDiscarded, run.AverageTPS, run.PeakTPS,
		string(latencyJSON), string(preconfJSON), string(pendingLatencyJSON), run.Status, run.ErrorMessage,
		run.BlockCount, run.TotalGasUsed, run.AvgFillRate, run.PeakMgasPerSec, run.AvgMgasPerSec,
		string(tipHistogramJSON), string(txTypeMetricsJSON), run.AccountsActive, run.AccountsFunded,
		run.OnChainFirstBlock, run.OnChainLastBlock, run.OnChainTxCount, run.OnChainGasUsed,
		run.OnChainMgasPerSec, run.OnChainTps, run.OnChainDurationSecs,
		string(environmentJSON), string(verificationJSON),
		string(deployedContractsJSON), string(testAccountsJSON), id)

	return err
}

// GetTestRun retrieves a single test run by ID.
func (s *SQLiteStorage) GetTestRun(ctx context.Context, id string) (*TestRun, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, started_at, completed_at, pattern, transaction_type, duration_ms,
			tx_sent, tx_confirmed, tx_failed, COALESCE(tx_discarded, 0), average_tps, peak_tps,
			latency_stats, preconf_latency_stats, pending_latency_stats, config, status, error_message, tx_logging_enabled,
			COALESCE(execution_layer, 'reth'),
			COALESCE(block_count, 0), COALESCE(total_gas_used, 0), COALESCE(avg_fill_rate, 0),
			COALESCE(peak_mgas_per_sec, 0), COALESCE(avg_mgas_per_sec, 0),
			custom_name, COALESCE(is_favorite, 0),
			tip_histogram, tx_type_metrics, COALESCE(accounts_active, 0), COALESCE(accounts_funded, 0),
			COALESCE(on_chain_first_block, 0), COALESCE(on_chain_last_block, 0),
			COALESCE(on_chain_tx_count, 0), COALESCE(on_chain_gas_used, 0),
			COALESCE(on_chain_mgas_per_sec, 0), COALESCE(on_chain_tps, 0), COALESCE(on_chain_duration_secs, 0),
			environment, verification,
			deployed_contracts, test_accounts
		FROM test_runs WHERE id = ?
	`, id)

	return s.scanTestRun(row)
}

// ListTestRuns returns a paginated list of test runs.
func (s *SQLiteStorage) ListTestRuns(ctx context.Context, limit, offset int) (*PaginatedTestRuns, error) {
	// Get total count
	var total int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM test_runs").Scan(&total)
	if err != nil {
		return nil, err
	}

	// Get paginated results - favorites first, then by date
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, started_at, completed_at, pattern, transaction_type, duration_ms,
			tx_sent, tx_confirmed, tx_failed, COALESCE(tx_discarded, 0), average_tps, peak_tps,
			latency_stats, preconf_latency_stats, pending_latency_stats, config, status, error_message, tx_logging_enabled,
			COALESCE(execution_layer, 'reth'),
			COALESCE(block_count, 0), COALESCE(total_gas_used, 0), COALESCE(avg_fill_rate, 0),
			COALESCE(peak_mgas_per_sec, 0), COALESCE(avg_mgas_per_sec, 0),
			custom_name, COALESCE(is_favorite, 0),
			tip_histogram, tx_type_metrics, COALESCE(accounts_active, 0), COALESCE(accounts_funded, 0),
			COALESCE(on_chain_first_block, 0), COALESCE(on_chain_last_block, 0),
			COALESCE(on_chain_tx_count, 0), COALESCE(on_chain_gas_used, 0),
			COALESCE(on_chain_mgas_per_sec, 0), COALESCE(on_chain_tps, 0), COALESCE(on_chain_duration_secs, 0),
			environment, verification,
			deployed_contracts, test_accounts
		FROM test_runs
		ORDER BY is_favorite DESC, started_at DESC
		LIMIT ? OFFSET ?
	`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []TestRun
	for rows.Next() {
		run, err := s.scanTestRunFromRows(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, *run)
	}

	return &PaginatedTestRuns{
		Runs:   runs,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	}, nil
}

// DeleteTestRun deletes a test run and all associated data.
func (s *SQLiteStorage) DeleteTestRun(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM test_runs WHERE id = ?", id)
	return err
}

// UpdateTestRunMetadata updates the custom name and/or favorite status of a test run.
func (s *SQLiteStorage) UpdateTestRunMetadata(ctx context.Context, id string, update *TestRunMetadataUpdate) error {
	// Build update query dynamically based on which fields are provided
	var updates []string
	var args []interface{}

	if update.CustomName != nil {
		updates = append(updates, "custom_name = ?")
		args = append(args, *update.CustomName)
	}
	if update.IsFavorite != nil {
		updates = append(updates, "is_favorite = ?")
		if *update.IsFavorite {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}

	if len(updates) == 0 {
		return nil // Nothing to update
	}

	args = append(args, id)
	query := fmt.Sprintf("UPDATE test_runs SET %s WHERE id = ?", joinStrings(updates, ", "))

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return fmt.Errorf("test run not found: %s", id)
	}

	return nil
}

// joinStrings joins strings with a separator (avoiding strings package import).
func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}

// BulkInsertTimeSeries inserts multiple time-series points efficiently.
func (s *SQLiteStorage) BulkInsertTimeSeries(ctx context.Context, testID string, points []TimeSeriesPoint) error {
	if len(points) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO time_series (test_run_id, timestamp_ms, tx_sent, tx_confirmed, tx_failed, current_tps, target_tps, pending_count,
			gas_used, gas_limit, block_count, mgas_per_sec, fill_rate)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()

	for _, p := range points {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, err := stmt.ExecContext(ctx, testID, p.TimestampMs, p.TxSent, p.TxConfirmed, p.TxFailed, p.CurrentTPS, p.TargetTPS, p.PendingCount,
			p.GasUsed, p.GasLimit, p.BlockCount, p.MgasPerSec, p.FillRate)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetTimeSeries retrieves time-series data for a test run.
func (s *SQLiteStorage) GetTimeSeries(ctx context.Context, testID string) ([]TimeSeriesPoint, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT timestamp_ms, tx_sent, tx_confirmed, tx_failed, current_tps, target_tps, pending_count,
			COALESCE(gas_used, 0), COALESCE(gas_limit, 0), COALESCE(block_count, 0), COALESCE(mgas_per_sec, 0), COALESCE(fill_rate, 0)
		FROM time_series
		WHERE test_run_id = ?
		ORDER BY timestamp_ms
	`, testID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []TimeSeriesPoint
	for rows.Next() {
		var p TimeSeriesPoint
		err := rows.Scan(&p.TimestampMs, &p.TxSent, &p.TxConfirmed, &p.TxFailed, &p.CurrentTPS, &p.TargetTPS, &p.PendingCount,
			&p.GasUsed, &p.GasLimit, &p.BlockCount, &p.MgasPerSec, &p.FillRate)
		if err != nil {
			return nil, err
		}
		points = append(points, p)
	}

	return points, nil
}

// BulkInsertTxLogs inserts transaction logs using a single transaction for maximum speed.
// PERFORMANCE: Uses single transaction for ALL rows instead of batching into separate transactions.
// This is 10-50x faster for large inserts because we only pay the fsync cost once.
func (s *SQLiteStorage) BulkInsertTxLogs(ctx context.Context, testID string, logs []TxLogEntry) error {
	if len(logs) == 0 {
		return nil
	}

	// Use a single transaction for ALL rows - much faster than multiple small transactions
	// The fsync overhead dominates, so doing it once instead of 100+ times is a huge win
	// Note: WAL mode + _sync=NORMAL is already set in DSN for good performance
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO tx_logs (test_run_id, tx_hash, sent_at_ms, confirmed_at_ms, preconf_at_ms,
			confirm_latency_ms, preconf_latency_ms, status, error_reason, from_account, nonce, gas_tip_gwei)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, log := range logs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, err := stmt.ExecContext(ctx, testID, log.TxHash, log.SentAtMs,
			nullInt64(log.ConfirmedAtMs), nullInt64(log.PreconfAtMs),
			nullInt64(log.ConfirmLatencyMs), nullInt64(log.PreconfLatencyMs),
			log.Status, nullString(log.ErrorReason), log.FromAccount, log.Nonce, log.GasTipGwei)
		if err != nil {
			return err
		}
	}

	// Single commit at the end - this is where the fsync happens
	return tx.Commit()
}

// GetTxLogs retrieves paginated transaction logs for a test run.
func (s *SQLiteStorage) GetTxLogs(ctx context.Context, testID string, limit, offset int) (*PaginatedTxLogs, error) {
	// Get total count
	var total int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tx_logs WHERE test_run_id = ?", testID).Scan(&total)
	if err != nil {
		return nil, err
	}

	// Get paginated results
	rows, err := s.db.QueryContext(ctx, `
		SELECT tx_hash, sent_at_ms, confirmed_at_ms, preconf_at_ms,
			confirm_latency_ms, preconf_latency_ms, status, error_reason, from_account, nonce, gas_tip_gwei
		FROM tx_logs
		WHERE test_run_id = ?
		ORDER BY sent_at_ms
		LIMIT ? OFFSET ?
	`, testID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []TxLogEntry
	for rows.Next() {
		var log TxLogEntry
		var confirmedAt, preconfAt, confirmLatency, preconfLatency sql.NullInt64
		var errorReason sql.NullString

		err := rows.Scan(&log.TxHash, &log.SentAtMs, &confirmedAt, &preconfAt,
			&confirmLatency, &preconfLatency, &log.Status, &errorReason, &log.FromAccount, &log.Nonce, &log.GasTipGwei)
		if err != nil {
			return nil, err
		}

		if confirmedAt.Valid {
			log.ConfirmedAtMs = confirmedAt.Int64
		}
		if preconfAt.Valid {
			log.PreconfAtMs = preconfAt.Int64
		}
		if confirmLatency.Valid {
			log.ConfirmLatencyMs = confirmLatency.Int64
		}
		if preconfLatency.Valid {
			log.PreconfLatencyMs = preconfLatency.Int64
		}
		if errorReason.Valid {
			log.ErrorReason = errorReason.String
		}

		logs = append(logs, log)
	}

	return &PaginatedTxLogs{
		Transactions: logs,
		Total:        total,
		Limit:        limit,
		Offset:       offset,
	}, nil
}

// GetTxLogByHash retrieves a single transaction log by hash.
func (s *SQLiteStorage) GetTxLogByHash(ctx context.Context, txHash string) (*TxLogEntry, error) {
	var log TxLogEntry
	var confirmedAt, preconfAt, confirmLatency, preconfLatency sql.NullInt64
	var errorReason sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT tx_hash, sent_at_ms, confirmed_at_ms, preconf_at_ms,
			confirm_latency_ms, preconf_latency_ms, status, error_reason, from_account, nonce, gas_tip_gwei
		FROM tx_logs
		WHERE tx_hash = ?
	`, txHash).Scan(&log.TxHash, &log.SentAtMs, &confirmedAt, &preconfAt,
		&confirmLatency, &preconfLatency, &log.Status, &errorReason, &log.FromAccount, &log.Nonce, &log.GasTipGwei)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if confirmedAt.Valid {
		log.ConfirmedAtMs = confirmedAt.Int64
	}
	if preconfAt.Valid {
		log.PreconfAtMs = preconfAt.Int64
	}
	if confirmLatency.Valid {
		log.ConfirmLatencyMs = confirmLatency.Int64
	}
	if preconfLatency.Valid {
		log.PreconfLatencyMs = preconfLatency.Int64
	}
	if errorReason.Valid {
		log.ErrorReason = errorReason.String
	}

	return &log, nil
}

// Helper functions

func (s *SQLiteStorage) scanTestRun(row *sql.Row) (*TestRun, error) {
	var run TestRun
	var completedAt sql.NullTime
	var latencyJSON, preconfJSON, pendingLatencyJSON, configJSON sql.NullString
	var errorMsg sql.NullString
	var customName sql.NullString
	var isFavorite int
	var tipHistogramJSON, txTypeMetricsJSON sql.NullString
	var environmentJSON, verificationJSON sql.NullString
	var deployedContractsJSON, testAccountsJSON sql.NullString

	err := row.Scan(&run.ID, &run.StartedAt, &completedAt, &run.Pattern, &run.TransactionType, &run.DurationMs,
		&run.TxSent, &run.TxConfirmed, &run.TxFailed, &run.TxDiscarded, &run.AverageTPS, &run.PeakTPS,
		&latencyJSON, &preconfJSON, &pendingLatencyJSON, &configJSON, &run.Status, &errorMsg, &run.TxLoggingEnabled,
		&run.ExecutionLayer,
		&run.BlockCount, &run.TotalGasUsed, &run.AvgFillRate, &run.PeakMgasPerSec, &run.AvgMgasPerSec,
		&customName, &isFavorite,
		&tipHistogramJSON, &txTypeMetricsJSON, &run.AccountsActive, &run.AccountsFunded,
		&run.OnChainFirstBlock, &run.OnChainLastBlock, &run.OnChainTxCount, &run.OnChainGasUsed,
		&run.OnChainMgasPerSec, &run.OnChainTps, &run.OnChainDurationSecs,
		&environmentJSON, &verificationJSON,
		&deployedContractsJSON, &testAccountsJSON)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if completedAt.Valid {
		run.CompletedAt = &completedAt.Time
	}
	if errorMsg.Valid {
		run.ErrorMessage = errorMsg.String
	}
	if customName.Valid {
		run.CustomName = &customName.String
	}
	run.IsFavorite = isFavorite == 1

	if latencyJSON.Valid && latencyJSON.String != "" {
		run.LatencyStats = &types.LatencyStats{}
		unmarshalJSON(latencyJSON.String, run.LatencyStats, "latency_stats", run.ID)
	}
	if preconfJSON.Valid && preconfJSON.String != "" {
		run.PreconfLatency = &types.LatencyStats{}
		unmarshalJSON(preconfJSON.String, run.PreconfLatency, "preconf_latency", run.ID)
	}
	if pendingLatencyJSON.Valid && pendingLatencyJSON.String != "" {
		run.PendingLatency = &types.LatencyStats{}
		unmarshalJSON(pendingLatencyJSON.String, run.PendingLatency, "pending_latency", run.ID)
	}
	if configJSON.Valid && configJSON.String != "" {
		run.Config = &types.StartTestRequest{}
		unmarshalJSON(configJSON.String, run.Config, "config", run.ID)
	}
	if tipHistogramJSON.Valid && tipHistogramJSON.String != "" {
		unmarshalJSON(tipHistogramJSON.String, &run.TipHistogram, "tip_histogram", run.ID)
	}
	if txTypeMetricsJSON.Valid && txTypeMetricsJSON.String != "" {
		unmarshalJSON(txTypeMetricsJSON.String, &run.TxTypeMetrics, "tx_type_metrics", run.ID)
	}
	if environmentJSON.Valid && environmentJSON.String != "" {
		run.Environment = &EnvironmentSnapshot{}
		unmarshalJSON(environmentJSON.String, run.Environment, "environment", run.ID)
	}
	if verificationJSON.Valid && verificationJSON.String != "" {
		run.Verification = &VerificationResult{}
		unmarshalJSON(verificationJSON.String, run.Verification, "verification", run.ID)
	}
	if deployedContractsJSON.Valid && deployedContractsJSON.String != "" {
		unmarshalJSON(deployedContractsJSON.String, &run.DeployedContracts, "deployed_contracts", run.ID)
	}
	if testAccountsJSON.Valid && testAccountsJSON.String != "" {
		run.TestAccounts = &TestAccountsInfo{}
		unmarshalJSON(testAccountsJSON.String, run.TestAccounts, "test_accounts", run.ID)
	}

	return &run, nil
}

func (s *SQLiteStorage) scanTestRunFromRows(rows *sql.Rows) (*TestRun, error) {
	var run TestRun
	var completedAt sql.NullTime
	var latencyJSON, preconfJSON, pendingLatencyJSON, configJSON sql.NullString
	var errorMsg sql.NullString
	var customName sql.NullString
	var isFavorite int
	var tipHistogramJSON, txTypeMetricsJSON sql.NullString
	var environmentJSON, verificationJSON sql.NullString
	var deployedContractsJSON, testAccountsJSON sql.NullString

	err := rows.Scan(&run.ID, &run.StartedAt, &completedAt, &run.Pattern, &run.TransactionType, &run.DurationMs,
		&run.TxSent, &run.TxConfirmed, &run.TxFailed, &run.TxDiscarded, &run.AverageTPS, &run.PeakTPS,
		&latencyJSON, &preconfJSON, &pendingLatencyJSON, &configJSON, &run.Status, &errorMsg, &run.TxLoggingEnabled,
		&run.ExecutionLayer,
		&run.BlockCount, &run.TotalGasUsed, &run.AvgFillRate, &run.PeakMgasPerSec, &run.AvgMgasPerSec,
		&customName, &isFavorite,
		&tipHistogramJSON, &txTypeMetricsJSON, &run.AccountsActive, &run.AccountsFunded,
		&run.OnChainFirstBlock, &run.OnChainLastBlock, &run.OnChainTxCount, &run.OnChainGasUsed,
		&run.OnChainMgasPerSec, &run.OnChainTps, &run.OnChainDurationSecs,
		&environmentJSON, &verificationJSON,
		&deployedContractsJSON, &testAccountsJSON)

	if err != nil {
		return nil, err
	}

	if completedAt.Valid {
		run.CompletedAt = &completedAt.Time
	}
	if errorMsg.Valid {
		run.ErrorMessage = errorMsg.String
	}
	if customName.Valid {
		run.CustomName = &customName.String
	}
	run.IsFavorite = isFavorite == 1

	if latencyJSON.Valid && latencyJSON.String != "" {
		run.LatencyStats = &types.LatencyStats{}
		unmarshalJSON(latencyJSON.String, run.LatencyStats, "latency_stats", run.ID)
	}
	if preconfJSON.Valid && preconfJSON.String != "" {
		run.PreconfLatency = &types.LatencyStats{}
		unmarshalJSON(preconfJSON.String, run.PreconfLatency, "preconf_latency", run.ID)
	}
	if pendingLatencyJSON.Valid && pendingLatencyJSON.String != "" {
		run.PendingLatency = &types.LatencyStats{}
		unmarshalJSON(pendingLatencyJSON.String, run.PendingLatency, "pending_latency", run.ID)
	}
	if configJSON.Valid && configJSON.String != "" {
		run.Config = &types.StartTestRequest{}
		unmarshalJSON(configJSON.String, run.Config, "config", run.ID)
	}
	if tipHistogramJSON.Valid && tipHistogramJSON.String != "" {
		unmarshalJSON(tipHistogramJSON.String, &run.TipHistogram, "tip_histogram", run.ID)
	}
	if txTypeMetricsJSON.Valid && txTypeMetricsJSON.String != "" {
		unmarshalJSON(txTypeMetricsJSON.String, &run.TxTypeMetrics, "tx_type_metrics", run.ID)
	}
	if environmentJSON.Valid && environmentJSON.String != "" {
		run.Environment = &EnvironmentSnapshot{}
		unmarshalJSON(environmentJSON.String, run.Environment, "environment", run.ID)
	}
	if verificationJSON.Valid && verificationJSON.String != "" {
		run.Verification = &VerificationResult{}
		unmarshalJSON(verificationJSON.String, run.Verification, "verification", run.ID)
	}
	if deployedContractsJSON.Valid && deployedContractsJSON.String != "" {
		unmarshalJSON(deployedContractsJSON.String, &run.DeployedContracts, "deployed_contracts", run.ID)
	}
	if testAccountsJSON.Valid && testAccountsJSON.String != "" {
		run.TestAccounts = &TestAccountsInfo{}
		unmarshalJSON(testAccountsJSON.String, run.TestAccounts, "test_accounts", run.ID)
	}

	return &run, nil
}

func nullInt64(v int64) sql.NullInt64 {
	if v == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: v, Valid: true}
}

func nullString(v string) sql.NullString {
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}
