package cache

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/commons/logger"
	_ "github.com/mattn/go-sqlite3"
	"github.com/samber/lo"
)

var (
	// ErrCacheDisabled indicates caching is disabled
	ErrCacheDisabled = errors.New("caching is disabled")
	// ErrNotFound indicates the entry was not found in cache
	ErrNotFound = errors.New("cache entry not found")
)

// Config holds cache configuration
type Config struct {
	DBPath  string        // Database file path (default: ~/.cache/commons-llm.db)
	TTL     time.Duration // Cache time-to-live
	NoCache bool          // Disable caching
	Debug   bool          // Enable debug output
}

// Entry represents a cached LLM response
type Entry struct {
	ID               int64
	CacheKey         string
	PromptHash       string
	Model            string
	Prompt           string
	Response         string
	Error            string
	TokensInput      int
	TokensOutput     int
	TokensReasoning  int
	TokensCacheRead  int
	TokensCacheWrite int
	TokensTotal      int
	CostUSD          float64
	DurationMS       int64
	Provider         string
	Temperature      float64
	MaxTokens        int
	CreatedAt        time.Time
	AccessedAt       time.Time
	ExpiresAt        *time.Time
}

// StatsEntry represents aggregated statistics
type StatsEntry struct {
	Model                 string
	Provider              string
	TotalRequests         int64
	CacheHits             int64
	CacheMisses           int64
	ErrorCount            int64
	TotalInputTokens      int64
	TotalOutputTokens     int64
	TotalReasoningTokens  int64
	TotalCacheReadTokens  int64
	TotalCacheWriteTokens int64
	TotalCost             float64
	AvgDurationMS         int64
	FirstRequest          time.Time
	LastRequest           time.Time
}

// Cache manages LLM response caching in SQLite
type Cache struct {
	db     *sql.DB
	config Config
}

func (c Cache) GetTTL() time.Duration {
	return c.config.TTL
}

// New creates a new cache instance
func New(config Config) (*Cache, error) {
	if config.DBPath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		config.DBPath = filepath.Join(homeDir, ".cache", "commons-llm.db")
	}

	// Ensure cache directory exists
	cacheDir := filepath.Dir(config.DBPath)
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	// Open database
	db, err := sql.Open("sqlite3", config.DBPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Set pragmas for performance
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA cache_size = -64000", // 64MB cache
		"PRAGMA busy_timeout = 5000",
	}
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to set pragma %s: %w", pragma, err)
		}
	}

	cache := &Cache{
		db:     db,
		config: config,
	}

	// Initialize schema
	if err := cache.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	// Clean expired entries periodically
	go cache.cleanupExpired()

	return cache, nil
}

// Close closes the database connection
func (c *Cache) Close() error {
	if err := c.db.Close(); err != nil {
		return fmt.Errorf("failed to close database: %w", err)
	}
	return nil
}

// generateCacheKey creates a unique key for cache lookup
func generateCacheKey(prompt, model string) string {
	data := fmt.Sprintf("%s|%s", prompt, model)
	hash := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", hash)
}

// Get retrieves a cached response
func (c *Cache) Get(prompt, model string) (*Entry, error) {
	if c.config.NoCache {
		return nil, ErrCacheDisabled
	}

	cacheKey := generateCacheKey(prompt, model)

	query := `
		SELECT id, cache_key, prompt_hash, model, prompt, response, error,
		       tokens_input, tokens_output, tokens_reasoning,
		       tokens_cache_read, tokens_cache_write, tokens_total,
		       cost_usd, duration_ms, provider, temperature, max_tokens,
		       created_at, accessed_at, expires_at
		FROM llm_cache
		WHERE cache_key = ?
		  AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
		ORDER BY created_at DESC
		LIMIT 1
	`

	var entry Entry
	var expiresAt sql.NullTime
	err := c.db.QueryRow(query, cacheKey).Scan(
		&entry.ID, &entry.CacheKey, &entry.PromptHash, &entry.Model,
		&entry.Prompt, &entry.Response, &entry.Error,
		&entry.TokensInput, &entry.TokensOutput, &entry.TokensReasoning,
		&entry.TokensCacheRead, &entry.TokensCacheWrite, &entry.TokensTotal,
		&entry.CostUSD, &entry.DurationMS, &entry.Provider,
		&entry.Temperature, &entry.MaxTokens,
		&entry.CreatedAt, &entry.AccessedAt, &expiresAt,
	)

	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get cache entry: %w", err)
	}

	if expiresAt.Valid {
		entry.ExpiresAt = &expiresAt.Time
		// Double-check expiration in Go code
		if time.Now().After(expiresAt.Time) {
			return nil, ErrNotFound
		}
	}

	// Update access time
	_, _ = c.db.Exec("UPDATE llm_cache SET accessed_at = CURRENT_TIMESTAMP WHERE id = ?", entry.ID)

	return &entry, nil
}

// Set stores a response in the cache
func (c *Cache) Set(entry *Entry) error {

	if c.config.NoCache {
		return nil
	}

	entry.CreatedAt = time.Now()
	entry.CacheKey = generateCacheKey(entry.Prompt, entry.Model)
	entry.PromptHash = generateCacheKey(entry.Prompt, entry.Model)

	logger.Tracef("[%s] caching response for %s: (hash:%s)", entry.Model, lo.Ellipsis(entry.Prompt, 20), entry.PromptHash)

	// Calculate expiration
	var expiresAt *time.Time
	if c.config.TTL > 0 {
		exp := time.Now().Add(c.config.TTL)
		expiresAt = &exp
	}

	query := `
		INSERT OR REPLACE INTO llm_cache (
			cache_key, prompt_hash, model, prompt, response, error,
			tokens_input, tokens_output, tokens_reasoning,
			tokens_cache_read, tokens_cache_write, tokens_total,
			cost_usd, duration_ms, provider, temperature, max_tokens, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := c.db.Exec(query,
		entry.CacheKey, entry.PromptHash, entry.Model, entry.Prompt, entry.Response, entry.Error,
		entry.TokensInput, entry.TokensOutput, entry.TokensReasoning,
		entry.TokensCacheRead, entry.TokensCacheWrite, entry.TokensTotal,
		entry.CostUSD, entry.DurationMS, entry.Provider, entry.Temperature, entry.MaxTokens,
		expiresAt,
	)
	if err != nil {
		return fmt.Errorf("failed to set cache entry: %w", err)
	}

	// Update daily stats
	c.updateStats(entry)

	return nil
}

// Clear removes all cache entries
func (c *Cache) Clear() error {
	result, err := c.db.Exec("DELETE FROM llm_cache")
	if err != nil {
		return fmt.Errorf("failed to clear cache: %w", err)
	}

	rows, _ := result.RowsAffected()
	if c.config.Debug {
		fmt.Fprintf(os.Stderr, "Cleared %d cache entries\n", rows)
	}

	return nil
}

// GetStats retrieves aggregated statistics
func (c *Cache) GetStats() ([]StatsEntry, error) {
	query := `
		SELECT model, provider,
		       COUNT(*) as total_requests,
		       SUM(CASE WHEN error IS NULL OR error = '' THEN 1 ELSE 0 END) as successful_requests,
		       SUM(CASE WHEN error IS NOT NULL AND error != '' THEN 1 ELSE 0 END) as failed_requests,
		       SUM(tokens_input) as total_input_tokens,
		       SUM(tokens_output) as total_output_tokens,
		       SUM(tokens_reasoning) as total_reasoning_tokens,
		       SUM(tokens_cache_read) as total_cache_read_tokens,
		       SUM(tokens_cache_write) as total_cache_write_tokens,
		       SUM(cost_usd) as total_cost,
		       AVG(duration_ms) as avg_duration_ms,
		       MIN(created_at) as first_request,
		       MAX(created_at) as last_request
		FROM llm_cache
		GROUP BY model, provider
		ORDER BY total_requests DESC
	`

	rows, err := c.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to get stats: %w", err)
	}
	defer rows.Close()

	var stats []StatsEntry
	for rows.Next() {
		var s StatsEntry
		var providerNull sql.NullString
		var totalInputTokens, totalOutputTokens, totalReasoningTokens sql.NullInt64
		var totalCacheReadTokens, totalCacheWriteTokens sql.NullInt64

		err := rows.Scan(
			&s.Model, &providerNull,
			&s.TotalRequests, &s.CacheHits, &s.ErrorCount,
			&totalInputTokens, &totalOutputTokens, &totalReasoningTokens,
			&totalCacheReadTokens, &totalCacheWriteTokens,
			&s.TotalCost, &s.AvgDurationMS,
			&s.FirstRequest, &s.LastRequest,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan stats: %w", err)
		}

		if providerNull.Valid {
			s.Provider = providerNull.String
		}
		if totalInputTokens.Valid {
			s.TotalInputTokens = totalInputTokens.Int64
		}
		if totalOutputTokens.Valid {
			s.TotalOutputTokens = totalOutputTokens.Int64
		}
		if totalReasoningTokens.Valid {
			s.TotalReasoningTokens = totalReasoningTokens.Int64
		}
		if totalCacheReadTokens.Valid {
			s.TotalCacheReadTokens = totalCacheReadTokens.Int64
		}
		if totalCacheWriteTokens.Valid {
			s.TotalCacheWriteTokens = totalCacheWriteTokens.Int64
		}

		stats = append(stats, s)
	}

	return stats, nil
}

// cleanupExpired removes expired cache entries
func (c *Cache) cleanupExpired() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		query := "DELETE FROM llm_cache WHERE expires_at IS NOT NULL AND expires_at < CURRENT_TIMESTAMP"
		result, err := c.db.Exec(query)
		if err != nil {
			if c.config.Debug {
				fmt.Fprintf(os.Stderr, "Failed to cleanup expired entries: %v\n", err)
			}
			continue
		}

		if rows, _ := result.RowsAffected(); rows > 0 && c.config.Debug {
			fmt.Fprintf(os.Stderr, "Cleaned up %d expired cache entries\n", rows)
		}
	}
}

// updateStats updates daily statistics
func (c *Cache) updateStats(entry *Entry) {
	date := entry.CreatedAt.Format("2006-01-02")

	query := `
		INSERT INTO llm_stats (
			date, model, provider, request_count,
			total_input_tokens, total_output_tokens, total_reasoning_tokens,
			total_cache_read_tokens, total_cache_write_tokens,
			total_cost_usd
		) VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(date, model, provider) DO UPDATE SET
			request_count = request_count + 1,
			total_input_tokens = total_input_tokens + excluded.total_input_tokens,
			total_output_tokens = total_output_tokens + excluded.total_output_tokens,
			total_reasoning_tokens = total_reasoning_tokens + excluded.total_reasoning_tokens,
			total_cache_read_tokens = total_cache_read_tokens + excluded.total_cache_read_tokens,
			total_cache_write_tokens = total_cache_write_tokens + excluded.total_cache_write_tokens,
			total_cost_usd = total_cost_usd + excluded.total_cost_usd,
			updated_at = CURRENT_TIMESTAMP
	`

	// Set all token values to 0 - we only track cost now
	_, _ = c.db.Exec(query,
		date, entry.Model, entry.Provider,
		0, 0, 0, 0, 0,
		entry.CostUSD,
	)
}

// initSchema creates the database schema
func (c *Cache) initSchema() error {
	if _, err := c.db.Exec(embeddedSchema); err != nil {
		return fmt.Errorf("failed to execute schema: %w", err)
	}
	return nil
}
