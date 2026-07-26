package config

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage/keeper/cpa"
)

const (
	DefaultTimeZone               = "Asia/Shanghai"
	RedisQueueKeyDefault          = cpa.ManagementUsageQueueKey
	RedisQueueErrorBackoffDefault = 10 * time.Second
	MetadataSyncIntervalDefault   = 30 * time.Second
)

var (
	DefaultWorkDir      = filepath.Join(".", "data")
	DefaultSQLitePath   = filepath.Join(DefaultWorkDir, "app.db")
	DefaultLogDir       = filepath.Join(DefaultWorkDir, "logs")
	DefaultBackupDir    = filepath.Join(DefaultWorkDir, "backups")
	workDirDatabaseName = filepath.Base(DefaultSQLitePath)
	workDirLogsName     = filepath.Base(DefaultLogDir)
	workDirBackupsName  = filepath.Base(DefaultBackupDir)
)

type Config struct {
	// AppPort is the web service listen port.
	AppPort string
	// AppBasePath is the web service deployment subpath; empty means root.
	AppBasePath string
	// CPABaseURL is the CPA service base URL.
	CPABaseURL string
	// CPAManagementKey is the key used to access CPA management data.
	CPAManagementKey string
	// RedisQueueAddr is the TCP address of the CPA management data stream; empty derives from CPA_BASE_URL.
	RedisQueueAddr string
	// RedisQueueTLS controls whether the Redis queue connection uses TLS.
	RedisQueueTLS bool
	// RedisQueueKey is the CPA usage queue name.
	RedisQueueKey string
	// RedisQueueBatchSize is the max number of messages pulled per Redis LPOP.
	RedisQueueBatchSize int
	// RedisQueueIdleInterval is the delay before the next check when the Redis queue is empty.
	RedisQueueIdleInterval time.Duration
	// RedisQueueErrorBackoff is the fixed backoff after a transient Redis error.
	RedisQueueErrorBackoff time.Duration
	// MetadataSyncInterval is the fixed refresh interval for auth files and provider metadata.
	MetadataSyncInterval time.Duration
	// WorkDir is the application working directory; DB, logs, and backups are derived from it by default.
	WorkDir string
	// SQLitePath is the SQLite database file path.
	SQLitePath string
	// PostgresDSN is the Postgres connection string used by the usage repository Postgres opener.
	PostgresDSN string
	// BackupEnabled controls whether SQLite database backup files are written.
	BackupEnabled bool
	// BackupDir is the SQLite database backup directory.
	BackupDir string
	// BackupInterval is the minimum interval between backup writes.
	BackupInterval time.Duration
	// BackupRetentionDays is the backup file retention period in days.
	BackupRetentionDays int
	// RequestTimeout is the timeout for CPA HTTP and Redis TCP access.
	RequestTimeout time.Duration
	// TLSSkipVerify controls whether CPA HTTPS and Redis queue TLS certificate verification is skipped.
	TLSSkipVerify bool
	// LogLevel is the application log level.
	LogLevel string
	// LogFileEnabled controls whether persistent log files are written.
	LogFileEnabled bool
	// LogDir is the application log file directory.
	LogDir string
	// LogRetentionDays is log retention in days; 0 disables automatic cleanup.
	LogRetentionDays int
	// AuthEnabled controls whether login protection is enabled.
	AuthEnabled bool
	// LoginPassword is the login password used when auth protection is enabled.
	LoginPassword string
	// AuthSessionTTL is the login session lifetime.
	AuthSessionTTL time.Duration
}

type LoadOptions struct {
	EnvFile string
}

var executableDir = func() (string, error) {
	executablePath, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(executablePath), nil
}

func LoadFromEnv() (*Config, error) {
	return Load(LoadOptions{})
}

func Load(options LoadOptions) (*Config, error) {
	envBaseDir, err := loadDotEnv(options)
	if err != nil {
		return nil, err
	}
	if err := applyProjectTimeZone(); err != nil {
		return nil, err
	}

	redisQueueBatchSize, err := getInt("REDIS_QUEUE_BATCH_SIZE", 1000)
	if err != nil {
		return nil, err
	}
	if redisQueueBatchSize <= 0 {
		return nil, fmt.Errorf("REDIS_QUEUE_BATCH_SIZE must be positive")
	}

	redisQueueIdleInterval, err := getDuration("REDIS_QUEUE_IDLE_INTERVAL", time.Second)
	if err != nil {
		return nil, err
	}
	if redisQueueIdleInterval <= 0 {
		return nil, fmt.Errorf("REDIS_QUEUE_IDLE_INTERVAL must be positive")
	}

	requestTimeout, err := getDuration("REQUEST_TIMEOUT", 30*time.Second)
	if err != nil {
		return nil, err
	}

	backupEnabled, err := getBool("BACKUP_ENABLED", true)
	if err != nil {
		return nil, err
	}

	backupInterval, err := getDuration("BACKUP_INTERVAL", 24*time.Hour)
	if err != nil {
		return nil, err
	}
	if backupInterval <= 0 {
		return nil, fmt.Errorf("BACKUP_INTERVAL must be positive")
	}

	backupRetentionDays, err := getInt("BACKUP_RETENTION_DAYS", 7)
	if err != nil {
		return nil, err
	}
	if backupRetentionDays < 0 {
		return nil, fmt.Errorf("BACKUP_RETENTION_DAYS must be non-negative")
	}

	logFileEnabled, err := getBool("LOG_FILE_ENABLED", true)
	if err != nil {
		return nil, err
	}
	logRetentionDays, err := getInt("LOG_RETENTION_DAYS", 7)
	if err != nil {
		return nil, err
	}
	if logRetentionDays < 0 {
		return nil, fmt.Errorf("LOG_RETENTION_DAYS must be non-negative")
	}

	authSessionTTL, err := getDuration("AUTH_SESSION_TTL", 7*24*time.Hour)
	if err != nil {
		return nil, err
	}
	if authSessionTTL <= 0 {
		return nil, fmt.Errorf("AUTH_SESSION_TTL must be positive")
	}

	tlsSkipVerify, err := getBool("TLS_SKIP_VERIFY", false)
	if err != nil {
		return nil, err
	}

	redisQueueTLS, err := getBool("REDIS_QUEUE_TLS", false)
	if err != nil {
		return nil, err
	}

	appBasePath, err := normalizeBasePath(strings.TrimSpace(os.Getenv("APP_BASE_PATH")))
	if err != nil {
		return nil, fmt.Errorf("APP_BASE_PATH is invalid: %w", err)
	}

	workDir := getString("WORK_DIR", DefaultWorkDir)

	cfg := &Config{
		AppPort:                getString("APP_PORT", "8080"),
		AppBasePath:            appBasePath,
		CPABaseURL:             strings.TrimSpace(os.Getenv("CPA_BASE_URL")),
		CPAManagementKey:       strings.TrimSpace(os.Getenv("CPA_MANAGEMENT_KEY")),
		RedisQueueAddr:         strings.TrimSpace(os.Getenv("REDIS_QUEUE_ADDR")),
		RedisQueueTLS:          redisQueueTLS,
		RedisQueueKey:          RedisQueueKeyDefault,
		RedisQueueBatchSize:    redisQueueBatchSize,
		RedisQueueIdleInterval: redisQueueIdleInterval,
		RedisQueueErrorBackoff: RedisQueueErrorBackoffDefault,
		MetadataSyncInterval:   MetadataSyncIntervalDefault,
		WorkDir:                workDir,
		SQLitePath:             filepath.Join(workDir, workDirDatabaseName),
		BackupEnabled:          backupEnabled,
		BackupDir:              filepath.Join(workDir, workDirBackupsName),
		BackupInterval:         backupInterval,
		BackupRetentionDays:    backupRetentionDays,
		RequestTimeout:         requestTimeout,
		TLSSkipVerify:          tlsSkipVerify,
		LogLevel:               getString("LOG_LEVEL", "info"),
		LogFileEnabled:         logFileEnabled,
		LogDir:                 filepath.Join(workDir, workDirLogsName),
		LogRetentionDays:       logRetentionDays,
		AuthEnabled:            false,
		LoginPassword:          "",
		AuthSessionTTL:         authSessionTTL,
	}
	cfg.resolveRelativePaths(envBaseDir)

	return cfg, nil
}

func applyProjectTimeZone() error {
	zoneName := strings.TrimSpace(os.Getenv("TZ"))
	if zoneName == "" {
		zoneName = DefaultTimeZone
		if err := os.Setenv("TZ", zoneName); err != nil {
			return fmt.Errorf("set default TZ: %w", err)
		}
	}
	location, err := time.LoadLocation(zoneName)
	if err != nil {
		return fmt.Errorf("TZ is invalid: %w", err)
	}
	time.Local = location
	return nil
}

func loadDotEnv(options LoadOptions) (string, error) {
	if strings.TrimSpace(options.EnvFile) != "" {
		return loadDotEnvFile(options.EnvFile, true)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	if loaded, err := loadOptionalDotEnv(filepath.Join(cwd, ".env")); err != nil || loaded {
		if loaded {
			return cwd, err
		}
		return "", err
	}

	exeDir, err := executableDir()
	if err != nil {
		return "", fmt.Errorf("get executable directory: %w", err)
	}
	loaded, err := loadOptionalDotEnv(filepath.Join(exeDir, ".env"))
	if loaded {
		return exeDir, err
	}
	return "", err
}

func loadOptionalDotEnv(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat .env: %w", err)
	}
	if err := godotenv.Overload(path); err != nil {
		return false, fmt.Errorf("load .env: %w", err)
	}
	return true, nil
}

func loadDotEnvFile(path string, required bool) (string, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) && !required {
			return "", nil
		}
		return "", fmt.Errorf("stat env file: %w", err)
	}
	if err := godotenv.Overload(path); err != nil {
		return "", fmt.Errorf("load env file: %w", err)
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve env file path: %w", err)
	}
	return filepath.Dir(absolutePath), nil
}

func (cfg *Config) resolveRelativePaths(baseDir string) {
	if baseDir == "" {
		return
	}
	cfg.WorkDir = resolveRelativePath(baseDir, cfg.WorkDir)
	cfg.SQLitePath = resolveRelativePath(baseDir, cfg.SQLitePath)
	cfg.LogDir = resolveRelativePath(baseDir, cfg.LogDir)
	cfg.BackupDir = resolveRelativePath(baseDir, cfg.BackupDir)
}

func resolveRelativePath(baseDir, value string) string {
	if value == "" || filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(baseDir, value)
}

func normalizeBasePath(value string) (string, error) {
	if value == "" || value == "/" {
		return "", nil
	}
	if !strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("must start with '/'")
	}

	normalized := path.Clean(value)
	if normalized == "." || normalized == "/" {
		return "", nil
	}
	if !strings.HasPrefix(normalized, "/") {
		normalized = "/" + normalized
	}
	return normalized, nil
}

func getString(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func getDuration(key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid duration: %w", key, err)
	}
	return duration, nil
}

func getBool(key string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a valid bool: %w", key, err)
	}
	return parsed, nil
}

func getInt(key string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid integer: %w", key, err)
	}
	return parsed, nil
}
