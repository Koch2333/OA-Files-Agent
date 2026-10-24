// Package dbconfig decides which database the server uses and persists administrator-chosen settings.
//
// Precedence: environment (OA_DB_PROVIDER / OA_DATABASE_URL / OA_DB_DSN, read-only in the UI) > the settings file
// <data dir>/database.json (written by the admin API) > the built-in default (the embedded PostgreSQL of the
// Windows standalone install). Passwords and client keys are sealed with internal/secrets (AES-GCM) and never leave
// this package in clear text except to open a connection.
package dbconfig

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"oa-agent/backend/internal/secrets"
	"oa-agent/backend/internal/sqldb"
)

type Source string

const (
	SourceEnv      Source = "env"
	SourceFile     Source = "file"
	SourceEmbedded Source = "embedded"
	SourceNone     Source = "none"
)

// FileName is the settings file inside the data directory.
const FileName = "database.json"

// Entry is a stored connection setting; secrets are sealed.
type Entry struct {
	Provider      string `json:"provider"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Name          string `json:"name"`
	User          string `json:"user"`
	Schema        string `json:"schema,omitempty"`
	TLSMode       string `json:"tls_mode,omitempty"`
	CAPEM         string `json:"ca_pem,omitempty"`
	ServerName    string `json:"server_name,omitempty"`
	ClientCertPEM string `json:"client_cert_pem,omitempty"`
	PasswordEnc   string `json:"password_enc,omitempty"`   // base64(AES-GCM)
	ClientKeyEnc  string `json:"client_key_enc,omitempty"` // base64(AES-GCM)
}

// Pending is a saved configuration that takes effect on the next start, and only if every gate is still green.
type Pending struct {
	Entry
	SavedAt    time.Time  `json:"saved_at"`
	SavedBy    string     `json:"saved_by"`
	Validated  bool       `json:"validated"`
	Approved   bool       `json:"approved"`
	ApprovedAt *time.Time `json:"approved_at,omitempty"`
	ApprovedBy string     `json:"approved_by,omitempty"`
	// CurrentHasData records that the installation had data in the database it was running on when this was saved.
	CurrentHasData bool `json:"current_has_data"`
	// DataMigrated is set only by the cross-database migration workflow after a verified copy.
	DataMigrated      bool   `json:"data_migrated"`
	SourceFingerprint string `json:"source_fingerprint,omitempty"`
}

// ApplyRecord remembers what happened to the last pending configuration at start-up.
type ApplyRecord struct {
	At      time.Time `json:"at"`
	Applied bool      `json:"applied"`
	Reason  string    `json:"reason"`
}

// File is the content of database.json.
type File struct {
	Version   int          `json:"version"`
	Active    *Entry       `json:"active,omitempty"`
	Pending   *Pending     `json:"pending,omitempty"`
	LastApply *ApplyRecord `json:"last_apply,omitempty"`
}

var mu sync.Mutex

func path(dir string) string { return filepath.Join(dir, FileName) }

// Load reads the settings file; a missing file is an empty File.
func Load(dir string) (File, error) {
	var f File
	b, err := os.ReadFile(path(dir))
	if errors.Is(err, os.ErrNotExist) {
		return File{Version: 1}, nil
	}
	if err != nil {
		return f, err
	}
	if err = json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("%s is damaged: %w", FileName, err)
	}
	return f, nil
}

// Save writes the file atomically with mode 0600.
func Save(dir string, f File) error {
	mu.Lock()
	defer mu.Unlock()
	f.Version = 1
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".database-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err = os.Chmod(name, 0o600); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err = tmp.Write(b); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path(dir))
}

func seal(box *secrets.Box, v string) (string, error) {
	if v == "" {
		return "", nil
	}
	if box == nil {
		return "", errors.New("no secret key available to protect the database password")
	}
	enc, err := box.Seal([]byte(v))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(enc), nil
}

func unseal(box *secrets.Box, v string) (string, error) {
	if v == "" {
		return "", nil
	}
	if box == nil {
		return "", errors.New("no secret key available")
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return "", errors.New("stored database password is damaged")
	}
	p, err := box.Unseal(raw)
	if err != nil {
		return "", errors.New("stored database password cannot be decrypted (different secret key?)")
	}
	return string(p), nil
}

// EntryFromConfig seals the secrets of c.
func EntryFromConfig(box *secrets.Box, c sqldb.Config) (Entry, error) {
	pw, err := seal(box, c.Password)
	if err != nil {
		return Entry{}, err
	}
	ck, err := seal(box, c.ClientKeyPEM)
	if err != nil {
		return Entry{}, err
	}
	return Entry{Provider: string(c.Provider), Host: c.Host, Port: c.Port, Name: c.Name, User: c.User, Schema: c.Schema, TLSMode: c.TLSMode,
		CAPEM: c.CAPEM, ServerName: c.ServerName, ClientCertPEM: c.ClientCertPEM, PasswordEnc: pw, ClientKeyEnc: ck}, nil
}

// Config opens the sealed secrets and returns a usable configuration.
func (e Entry) Config(box *secrets.Box) (sqldb.Config, error) {
	p, err := sqldb.ParseProvider(e.Provider)
	if err != nil {
		return sqldb.Config{}, err
	}
	pw, err := unseal(box, e.PasswordEnc)
	if err != nil {
		return sqldb.Config{}, err
	}
	ck, err := unseal(box, e.ClientKeyEnc)
	if err != nil {
		return sqldb.Config{}, err
	}
	return sqldb.Config{Provider: p, Host: e.Host, Port: e.Port, Name: e.Name, User: e.User, Password: pw, Schema: e.Schema, TLSMode: e.TLSMode,
		CAPEM: e.CAPEM, ServerName: e.ServerName, ClientCertPEM: e.ClientCertPEM, ClientKeyPEM: ck}.Normalize(), nil
}

// HasPassword reports whether a password is stored (the value is never exposed).
func (e Entry) HasPassword() bool { return e.PasswordEnc != "" }

// Resolved is the outcome of Resolve.
type Resolved struct {
	Config sqldb.Config
	Source Source
	// ReadOnlyReason is non-empty when the admin API must not change the configuration.
	ReadOnlyReason string
}

// Configured reports whether a database has been chosen at all.
func (r Resolved) Configured() bool { return r.Source != SourceNone }

func envFile(getenv func(string) string, name string) (string, error) {
	p := strings.TrimSpace(getenv(name))
	if p == "" {
		return "", nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("%s: cannot read %s", name, p)
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

// FromEnv builds a configuration from the environment; ok=false when the environment names no database.
//
//	OA_DB_PROVIDER=postgres|mysql|mssql   OA_DB_HOST OA_DB_PORT OA_DB_NAME OA_DB_USER OA_DB_SCHEMA
//	OA_DB_PASSWORD or OA_DB_PASSWORD_FILE (Docker/Kubernetes secrets)
//	OA_DB_TLS_MODE=disable|require|verify-ca|verify-full  OA_DB_TLS_CA_FILE|OA_DB_TLS_CA_PEM  OA_DB_TLS_SERVER_NAME
//	OA_DB_TLS_CERT_FILE OA_DB_TLS_KEY_FILE
//	OA_DB_DSN (a complete URL for the provider) or the legacy OA_DATABASE_URL (PostgreSQL unless the scheme says otherwise)
func FromEnv(getenv func(string) string) (sqldb.Config, bool, error) {
	url := strings.TrimSpace(getenv("OA_DB_DSN"))
	if url == "" {
		url = strings.TrimSpace(getenv("OA_DATABASE_URL"))
	}
	provider := strings.TrimSpace(getenv("OA_DB_PROVIDER"))
	if url == "" && provider == "" {
		return sqldb.Config{}, false, nil
	}
	var c sqldb.Config
	if url != "" {
		var err error
		if c, err = sqldb.FromURL(url); err != nil {
			return c, true, err
		}
		if provider != "" {
			p, err := sqldb.ParseProvider(provider)
			if err != nil {
				return c, true, err
			}
			if p != c.Provider {
				return c, true, fmt.Errorf("OA_DB_PROVIDER=%s does not match the scheme of the database URL (%s)", p, c.Provider)
			}
		}
	} else {
		p, err := sqldb.ParseProvider(provider)
		if err != nil {
			return c, true, err
		}
		c.Provider = p
		c.Host = strings.TrimSpace(getenv("OA_DB_HOST"))
		c.Name = strings.TrimSpace(getenv("OA_DB_NAME"))
		c.User = strings.TrimSpace(getenv("OA_DB_USER"))
		c.Password = getenv("OA_DB_PASSWORD")
		if f, err := envFile(getenv, "OA_DB_PASSWORD_FILE"); err != nil {
			return c, true, err
		} else if f != "" {
			c.Password = f
		}
		if s := strings.TrimSpace(getenv("OA_DB_PORT")); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil {
				return c, true, errors.New("OA_DB_PORT must be a number")
			}
			c.Port = n
		}
	}
	if s := strings.TrimSpace(getenv("OA_DB_SCHEMA")); s != "" {
		c.Schema = s
	}
	if s := strings.TrimSpace(getenv("OA_DB_TLS_MODE")); s != "" {
		c.TLSMode = s
	}
	c.ServerName = strings.TrimSpace(getenv("OA_DB_TLS_SERVER_NAME"))
	c.CAPEM = getenv("OA_DB_TLS_CA_PEM")
	if f, err := envFile(getenv, "OA_DB_TLS_CA_FILE"); err != nil {
		return c, true, err
	} else if f != "" {
		c.CAPEM = f
	}
	if f, err := envFile(getenv, "OA_DB_TLS_CERT_FILE"); err != nil {
		return c, true, err
	} else if f != "" {
		c.ClientCertPEM = f
	}
	if f, err := envFile(getenv, "OA_DB_TLS_KEY_FILE"); err != nil {
		return c, true, err
	} else if f != "" {
		c.ClientKeyPEM = f
	}
	c = c.Normalize()
	if err := c.Validate(); err != nil {
		return c, true, fmt.Errorf("database settings: %w", err)
	}
	return c, true, nil
}

// Resolve picks the active configuration (without applying a pending one: see Promote).
func Resolve(getenv func(string) string, dir string, box *secrets.Box) (Resolved, error) {
	if c, ok, err := FromEnv(getenv); ok || err != nil {
		if err != nil {
			return Resolved{Source: SourceEnv}, err
		}
		return Resolved{Config: c, Source: SourceEnv, ReadOnlyReason: "The database is configured by environment variables (OA_DB_* / OA_DATABASE_URL). Change them in the deployment (.env / compose) and restart."}, nil
	}
	f, err := Load(dir)
	if err != nil {
		return Resolved{Source: SourceNone}, err
	}
	if f.Active != nil {
		c, err := f.Active.Config(box)
		if err != nil {
			return Resolved{Source: SourceFile}, err
		}
		return Resolved{Config: c, Source: SourceFile}, nil
	}
	return Resolved{Source: SourceNone}, nil
}
