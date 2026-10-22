package envfile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const sample = `# header
COMPOSE_FILE=docker-compose.yml:docker-compose.mounts.yml   # layers
OA_PORT=8080
OA_SECRET_KEY=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
OA_WORKER_TOKEN="quoted-token-value-1234567890abcdef"
OA_POSTGRES_PASSWORD=
OA_PORT=9090
OA_LLM_API_KEY='sk-abcdef123456'
OA_PUBLIC_URL=http://x:9090
`

func TestParseGetSet(t *testing.T) {
	f := Parse([]byte(sample))
	if v, _ := f.Get("OA_PORT"); v != "9090" {
		t.Errorf("last assignment must win, got %s", v)
	}
	if v, _ := f.Get("COMPOSE_FILE"); v != "docker-compose.yml:docker-compose.mounts.yml" {
		t.Errorf("comment not stripped: %q", v)
	}
	if v, _ := f.Get("OA_WORKER_TOKEN"); v != "quoted-token-value-1234567890abcdef" {
		t.Errorf("quotes: %q", v)
	}
	if f.Value("MISSING", "dflt") != "dflt" || f.Value("OA_POSTGRES_PASSWORD", "d") != "d" {
		t.Error("Value default")
	}
	f.Set("OA_PORT", "8081")
	if strings.Count(string(f.Bytes()), "OA_PORT=") != 1 {
		t.Errorf("duplicates must collapse:\n%s", f.Bytes())
	}
	f.Set("NEW_KEY", "v")
	out := string(f.Bytes())
	if !strings.HasPrefix(out, "# header\n") || !strings.HasSuffix(out, "NEW_KEY=v\n") {
		t.Errorf("comments/order lost:\n%s", out)
	}
	// untouched lines stay byte-identical (inline comment kept)
	if !strings.Contains(out, "COMPOSE_FILE=docker-compose.yml:docker-compose.mounts.yml   # layers") {
		t.Error("unrelated line modified")
	}
}

func TestMasking(t *testing.T) {
	f := Parse([]byte(sample))
	m := f.MaskedText()
	for _, secret := range []string{"0123456789abcdef0123", "quoted-token", "sk-abcdef"} {
		if strings.Contains(m, secret) {
			t.Errorf("secret %q leaked:\n%s", secret, m)
		}
	}
	if !strings.Contains(m, "OA_PORT=9090") || !strings.Contains(m, "# header") {
		t.Errorf("non-secrets must stay:\n%s", m)
	}
	if !strings.Contains(m, "OA_POSTGRES_PASSWORD=\n") {
		t.Error("empty secret should stay empty, not 'hidden'")
	}
	for _, k := range []string{"OA_SECRET_KEY", "OA_WORKER_TOKEN", "OA_POSTGRES_PASSWORD", "OA_LLM_API_KEY"} {
		if !IsSecret(k) {
			t.Errorf("%s should be secret", k)
		}
	}
	if IsSecret("OA_PORT") || IsSecret("OA_TZ") {
		t.Error("false positive")
	}
	if got := len(f.Secrets()); got != 3 {
		t.Errorf("Secrets() = %d", got)
	}
}

func TestEnviron(t *testing.T) {
	env := Parse([]byte(sample)).Environ()
	joined := strings.Join(env, "|")
	if !strings.Contains(joined, "OA_PORT=9090") || strings.Count(joined, "OA_PORT=") != 1 {
		t.Errorf("environ: %v", env)
	}
}

func TestValidateConfig(t *testing.T) {
	ok := [][2]string{
		{"OA_PORT", "8080"}, {"OA_PORT", "65535"}, {"OA_STORAGE", "off"}, {"OA_API_MEM", "2g"},
		{"OA_TZ", "Asia/Shanghai"}, {"OA_PUBLIC_URL", "https://oa.example.com"}, {"OA_PUBLIC_URL", "http://10.0.0.5:8080"},
		{"OA_LOG_LEVEL", ""}, {"OA_LOG_LEVEL", "debug"}, {"OA_VERSION", "1.2.3-rc1"}, {"OA_ACME_EMAIL", "a@b.co"},
		{"OA_LLM_BASE_URL", "https://api.example.com/v1"}, {"OA_LLM_TIMEOUT_SECONDS", "90"},
	}
	bad := [][2]string{
		{"OA_PORT", "0"}, {"OA_PORT", "65536"}, {"OA_PORT", "80a"}, {"OA_PORT", ""}, {"OA_PORT", "1 2"},
		{"OA_STORAGE", "maybe"}, {"OA_API_MEM", "lots"}, {"OA_TZ", "a b"}, {"OA_PUBLIC_URL", "ftp://x"},
		{"OA_PUBLIC_URL", "https://x/path"}, {"OA_LOG_LEVEL", "trace"}, {"OA_VERSION", "a/b"},
		{"OA_PORT", "80\nOA_SECRET_KEY=x"}, {"OA_TZ", "UTC$(x)"}, {"OA_LLM_MODEL", "a b"}, {"OA_VERSION", "x#y"},
		{"OA_ACME_EMAIL", "nope"},
	}
	for _, c := range ok {
		if err := ValidateConfig(c[0], c[1]); err != nil {
			t.Errorf("%v rejected: %v", c, err)
		}
	}
	for _, c := range bad {
		if err := ValidateConfig(c[0], c[1]); err == nil {
			t.Errorf("%v accepted", c)
		}
	}
	for _, k := range []string{"OA_SECRET_KEY", "OA_WORKER_TOKEN", "OA_POSTGRES_PASSWORD", "COMPOSE_FILE", "OA_DOMAIN", "OA_ARCHIVE_DIR", "OA_BIND_ADDR", "PATH"} {
		if err := ValidateConfig(k, "x"); !errors.Is(err, ErrNotWhitelisted) {
			t.Errorf("%s must not be whitelisted: %v", k, err)
		}
	}
}

func TestDomainRegexp(t *testing.T) {
	for _, d := range []string{"oa.example.com", "a-b.c.example.org"} {
		if !ValidDomain.MatchString(d) {
			t.Errorf("%s rejected", d)
		}
	}
	for _, d := range []string{"localhost", "http://x.com", "x.com:443", "-a.com", "a..com", "a.c", ""} {
		if ValidDomain.MatchString(d) {
			t.Errorf("%s accepted", d)
		}
	}
}

func TestSaveMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	f := Parse([]byte("A=1\n"))
	if err := f.Save(p); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	g, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := g.Get("A"); v != "1" {
		t.Error("reload")
	}
}
