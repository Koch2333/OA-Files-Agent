package sqldb

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// TLS modes, named after the PostgreSQL sslmode values.
const (
	TLSDisable    = "disable"
	TLSRequire    = "require"     // encrypted, certificate not verified
	TLSVerifyCA   = "verify-ca"   // encrypted, chain verified against the CA, host name not checked
	TLSVerifyFull = "verify-full" // encrypted, chain and host name verified
)

// Config describes how to reach one database. Password and ClientKeyPEM are secrets: they are never marshalled
// to JSON, never logged and never returned by the API.
type Config struct {
	Provider Provider `json:"provider"`
	Host     string   `json:"host,omitempty"`
	Port     int      `json:"port,omitempty"`
	Name     string   `json:"name,omitempty"` // database name
	User     string   `json:"user,omitempty"`
	Password string   `json:"-"`
	// Schema is the PostgreSQL schema (default public) or the SQL Server schema the account's default schema must
	// point at. MySQL has no separate schema: the database is the schema.
	Schema        string `json:"schema,omitempty"`
	TLSMode       string `json:"tls_mode,omitempty"`
	CAPEM         string `json:"ca_pem,omitempty"`
	ClientCertPEM string `json:"client_cert_pem,omitempty"`
	ClientKeyPEM  string `json:"-"`
	ServerName    string `json:"server_name,omitempty"`
	// URL is a complete PostgreSQL URL (the legacy OA_DATABASE_URL) or a provider DSN supplied by the environment.
	// When set it wins over the individual fields. It may contain a password: never expose it.
	URL      string `json:"-"`
	MaxConns int    `json:"max_conns,omitempty"`
}

// Normalize fills defaults.
func (c Config) Normalize() Config {
	if c.Port == 0 && c.URL == "" {
		c.Port = c.Provider.DefaultPort()
	}
	if c.TLSMode == "" {
		c.TLSMode = TLSDisable
		if c.CAPEM != "" {
			c.TLSMode = TLSVerifyFull
		}
	}
	if c.MaxConns <= 0 {
		c.MaxConns = 10
	}
	return c
}

// Validate checks the fields without touching the network.
func (c Config) Validate() error {
	if _, err := ParseProvider(string(c.Provider)); err != nil {
		return err
	}
	switch c.TLSMode {
	case "", TLSDisable, TLSRequire, TLSVerifyCA, TLSVerifyFull:
	default:
		return fmt.Errorf("tls_mode must be disable, require, verify-ca or verify-full")
	}
	if c.URL != "" {
		return nil
	}
	if strings.TrimSpace(c.Host) == "" {
		return errors.New("host is required")
	}
	if c.Port < 0 || c.Port > 65535 {
		return errors.New("port is out of range")
	}
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("database name is required")
	}
	if strings.TrimSpace(c.User) == "" {
		return errors.New("user name is required")
	}
	for _, v := range []string{c.Host, c.Name, c.User, c.Schema, c.ServerName} {
		if strings.ContainsAny(v, "\x00\r\n") {
			return errors.New("settings must not contain control characters")
		}
	}
	if c.CAPEM != "" {
		if _, err := parsePool(c.CAPEM); err != nil {
			return err
		}
	}
	if (c.ClientCertPEM == "") != (c.ClientKeyPEM == "") {
		return errors.New("client certificate and key must be given together")
	}
	return nil
}

// Fingerprint identifies the target database without containing any secret (host, port, name, user, provider).
func (c Config) Fingerprint() string {
	h, p, n, u := c.Host, c.Port, c.Name, c.User
	if c.URL != "" {
		if f, err := FromURL(c.URL); err == nil {
			h, p, n, u = f.Host, f.Port, f.Name, f.User
		}
	}
	return fmt.Sprintf("%s://%s@%s:%d/%s#%s", c.Provider, u, strings.ToLower(h), p, n, c.Schema)
}

// Address is "host:port" for display.
func (c Config) Address() string {
	if c.URL != "" {
		if f, err := FromURL(c.URL); err == nil {
			return net.JoinHostPort(f.Host, strconv.Itoa(f.Port))
		}
	}
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// FromURL parses a postgres:// , mysql:// or sqlserver:// URL into a Config (the URL is kept in Config.URL).
func FromURL(raw string) (Config, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return Config{}, errors.New("invalid database URL")
	}
	var p Provider
	switch strings.ToLower(u.Scheme) {
	case "postgres", "postgresql":
		p = Postgres
	case "mysql":
		p = MySQL
	case "sqlserver", "mssql":
		p = MSSQL
	default:
		return Config{}, fmt.Errorf("unsupported database URL scheme %q", u.Scheme)
	}
	c := Config{Provider: p, URL: raw, Host: u.Hostname()}
	if pt := u.Port(); pt != "" {
		c.Port, _ = strconv.Atoi(pt)
	}
	if c.Port == 0 {
		c.Port = p.DefaultPort()
	}
	if u.User != nil {
		c.User = u.User.Username()
		c.Password, _ = u.User.Password()
	}
	q := u.Query()
	switch p {
	case MSSQL:
		c.Name = q.Get("database")
		if c.Name == "" {
			c.Name = strings.TrimPrefix(u.Path, "/")
		}
	default:
		c.Name = strings.TrimPrefix(u.Path, "/")
	}
	if p == Postgres {
		switch q.Get("sslmode") {
		case "disable":
			c.TLSMode = TLSDisable
		case "verify-ca":
			c.TLSMode = TLSVerifyCA
		case "verify-full":
			c.TLSMode = TLSVerifyFull
		case "require", "prefer", "allow":
			c.TLSMode = TLSRequire
		}
		if sp := q.Get("search_path"); sp != "" && !strings.Contains(sp, ",") {
			c.Schema = strings.Trim(sp, `" `)
		}
	}
	return c, nil
}

func parsePool(pemText string) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(pemText)) {
		return nil, errors.New("the CA certificate is not valid PEM")
	}
	return pool, nil
}

// TLSConfig builds the client TLS configuration for the mode; nil means plaintext.
func (c Config) TLSConfig() (*tls.Config, error) {
	switch c.TLSMode {
	case "", TLSDisable:
		return nil, nil
	}
	host := c.Host
	if c.URL != "" && host == "" {
		if f, err := FromURL(c.URL); err == nil {
			host = f.Host
		}
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	if c.ServerName != "" {
		cfg.ServerName = c.ServerName
	}
	if c.ClientCertPEM != "" {
		cert, err := tls.X509KeyPair([]byte(c.ClientCertPEM), []byte(c.ClientKeyPEM))
		if err != nil {
			return nil, errors.New("the client certificate and key do not match or are not valid PEM")
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	var roots *x509.CertPool
	if c.CAPEM != "" {
		var err error
		if roots, err = parsePool(c.CAPEM); err != nil {
			return nil, err
		}
		cfg.RootCAs = roots
	}
	switch c.TLSMode {
	case TLSRequire:
		cfg.InsecureSkipVerify = true
	case TLSVerifyCA:
		cfg.InsecureSkipVerify = true
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("the server presented no certificate")
			}
			opts := x509.VerifyOptions{Roots: roots, Intermediates: x509.NewCertPool()}
			for _, ic := range cs.PeerCertificates[1:] {
				opts.Intermediates.AddCert(ic)
			}
			_, err := cs.PeerCertificates[0].Verify(opts)
			return err
		}
	case TLSVerifyFull:
	default:
		return nil, fmt.Errorf("unknown tls_mode %q", c.TLSMode)
	}
	return cfg, nil
}
