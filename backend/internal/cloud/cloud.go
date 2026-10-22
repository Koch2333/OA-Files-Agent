// Package cloud is the optional storage-connection layer: a small provider interface plus a catalogue of kinds
// (S3-compatible, Azure Blob, WebDAV, SFTP, FTP/FTPS, SMB, DingTalk Drive). Connections are used to import files
// from remote storage and to replicate archived files to it. Providers never overwrite and never delete user
// data (Remove exists only for the write-probe marker).
package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

// Entry is one item of a remote listing. Path is relative to the connection's base path ("/"-separated, no leading slash).
type Entry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size_bytes"`
	ModTime time.Time `json:"modified_at"`
}

// Provider is a connected remote storage rooted at the connection's base path. All paths are relative to that
// root and already validated with CleanRel by the caller; implementations must still never escape the root.
type Provider interface {
	// List returns the direct children of dir ("" = root). A missing dir is ErrNotFound.
	List(ctx context.Context, dir string) ([]Entry, error)
	// Stat returns ErrNotFound when the item does not exist.
	Stat(ctx context.Context, p string) (Entry, error)
	// Open streams a file; the size is -1 when unknown. The caller closes the reader.
	Open(ctx context.Context, p string) (io.ReadCloser, int64, error)
	// PutNew creates parent folders as needed and writes the file. It MUST fail with ErrExists, without
	// modifying anything, when the target already exists (never overwrite). size is the exact content length.
	PutNew(ctx context.Context, p string, r io.Reader, size int64) error
	// Remove deletes one file. It is used only to clean up the connection test's marker object.
	Remove(ctx context.Context, p string) error
	Close() error
}

var (
	ErrNotFound    = errors.New("not found")
	ErrExists      = errors.New("already exists")
	ErrAuth        = errors.New("authentication failed")
	ErrPermission  = errors.New("permission denied")
	ErrUnreachable = errors.New("cannot reach the server")
	ErrUnsupported = errors.New("not supported")
	ErrTooLarge    = errors.New("file too large")
)

// HostKeyError is returned by SFTP when no host key is pinned yet (trust on first use) or the pin does not match.
type HostKeyError struct {
	Fingerprint string // "SHA256:…"
	Changed     bool   // true: a pin exists and the server presented a different key
}

func (e *HostKeyError) Error() string {
	if e.Changed {
		return "SSH host key changed: " + e.Fingerprint
	}
	return "SSH host key not trusted yet: " + e.Fingerprint
}

// FieldType drives the generic connection form of the UI.
type FieldType string

const (
	TText     FieldType = "text"
	TNumber   FieldType = "number"
	TSelect   FieldType = "select"
	TPassword FieldType = "password" // secret, stored encrypted, never returned
	TTextarea FieldType = "textarea"
)

type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type ShowIf struct {
	Key    string `json:"key"`
	Equals string `json:"equals"`
}

type Field struct {
	Key         string    `json:"key"`
	Label       string    `json:"label"`
	Type        FieldType `json:"type"`
	Required    bool      `json:"required"`
	Secret      bool      `json:"secret"`
	Default     string    `json:"default"`
	Placeholder string    `json:"placeholder"`
	Help        string    `json:"help"`
	Options     []Option  `json:"options"`
	ShowIf      *ShowIf   `json:"show_if"`
	// Managed fields (e.g. the pinned SSH host key) are shown read-only and set by the server.
	Managed bool `json:"managed"`
}

// MarshalJSON keeps `options` an array (never null), as the contract requires.
func (f Field) MarshalJSON() ([]byte, error) {
	type plain Field
	if f.Options == nil {
		f.Options = []Option{}
	}
	return json.Marshal(plain(f))
}

// Kind describes one storage type. Factory connects with validated settings and decrypted secrets.
type Kind struct {
	ID          string                                                  `json:"id"`
	Name        string                                                  `json:"name"`
	Group       string                                                  `json:"group"` // "cloud" | "enterprise" | "gateway"
	Description string                                                  `json:"description"`
	DocsURL     string                                                  `json:"docs_url"`
	Fields      []Field                                                 `json:"fields"`
	Factory     func(ctx context.Context, s Settings) (Provider, error) `json:"-"`
}

// Settings is a connection's configuration as seen by a factory: plain settings and decrypted secrets.
type Settings struct {
	Values  map[string]string
	Secrets map[string]string
	// BasePath is the root every provider path is relative to (already cleaned, "" = storage root).
	BasePath string
	// Persist, when set, stores a changed secret (e.g. a rotated OAuth refresh token) back into the encrypted
	// connection record. It is nil for unsaved draft tests; providers must tolerate that and a failing call.
	Persist func(key, value string)
}

func (s Settings) Get(k string) string { return strings.TrimSpace(s.Values[k]) }
func (s Settings) Int(k string, def int) int {
	if n, err := strconv.Atoi(s.Get(k)); err == nil && n > 0 {
		return n
	}
	return def
}
func (s Settings) Bool(k string, def bool) bool {
	switch strings.ToLower(s.Get(k)) {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	}
	return def
}

var (
	mu    sync.RWMutex
	kinds = map[string]Kind{}
	order []string
)

// Register adds a kind (called from init functions of provider files).
func Register(k Kind) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := kinds[k.ID]; dup {
		panic("cloud: duplicate kind " + k.ID)
	}
	kinds[k.ID] = k
	order = append(order, k.ID)
}

// Kinds returns the catalogue in registration order (cloud, enterprise, gateway groups keep their own order).
func Kinds() []Kind {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Kind, 0, len(order))
	for _, id := range order {
		out = append(out, kinds[id])
	}
	return out
}

func KindByID(id string) (Kind, bool) {
	mu.RLock()
	defer mu.RUnlock()
	k, ok := kinds[id]
	return k, ok
}

// ValidationError is a user-correctable configuration problem.
type ValidationError struct{ Code, Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func bad(code, f string, a ...any) error { return &ValidationError{code, fmt.Sprintf(f, a...)} }

// Validate checks settings and secrets against the kind's field list: unknown keys, required fields (a required
// secret may be absent when haveSecrets says it is already stored), select options, number ranges, lengths and
// control characters. It returns the cleaned maps.
func Validate(kindID string, values, secretsIn map[string]string, haveSecrets map[string]bool) (map[string]string, map[string]string, error) {
	k, ok := KindByID(kindID)
	if !ok {
		return nil, nil, bad("UNKNOWN_KIND", "不支持的存储类型")
	}
	byKey := map[string]Field{}
	for _, f := range k.Fields {
		byKey[f.Key] = f
	}
	cleanV, cleanS := map[string]string{}, map[string]string{}
	for key, v := range values {
		f, ok := byKey[key]
		if !ok || f.Secret {
			return nil, nil, bad("UNKNOWN_FIELD", "未知配置项：%s", key)
		}
		v = strings.TrimSpace(v)
		if err := checkValue(f, v); err != nil {
			return nil, nil, err
		}
		if v != "" {
			cleanV[key] = v
		}
	}
	for key, v := range secretsIn {
		f, ok := byKey[key]
		if !ok || !f.Secret {
			return nil, nil, bad("UNKNOWN_FIELD", "未知凭据项：%s", key)
		}
		if f.Type != TTextarea {
			v = strings.TrimSpace(v)
		}
		if v == "" {
			continue
		}
		if utf8.RuneCountInString(v) > 8192 {
			return nil, nil, bad("INVALID_VALUE", "“%s”过长", f.Label)
		}
		cleanS[key] = v
	}
	for _, f := range k.Fields {
		if f.Managed {
			continue
		}
		if f.ShowIf != nil && cleanV[f.ShowIf.Key] != f.ShowIf.Equals && !(cleanV[f.ShowIf.Key] == "" && byKey[f.ShowIf.Key].Default == f.ShowIf.Equals) {
			if f.Secret {
				delete(cleanS, f.Key)
			} else {
				delete(cleanV, f.Key)
			}
			continue
		}
		if !f.Required {
			continue
		}
		if f.Secret {
			if cleanS[f.Key] == "" && !haveSecrets[f.Key] {
				return nil, nil, bad("REQUIRED", "请填写“%s”", f.Label)
			}
		} else if cleanV[f.Key] == "" && f.Default == "" {
			return nil, nil, bad("REQUIRED", "请填写“%s”", f.Label)
		}
	}
	return cleanV, cleanS, nil
}

func checkValue(f Field, v string) error {
	if utf8.RuneCountInString(v) > 1024 {
		return bad("INVALID_VALUE", "“%s”过长", f.Label)
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return bad("INVALID_VALUE", "“%s”含有控制字符", f.Label)
		}
	}
	switch f.Type {
	case TNumber:
		if v == "" {
			return nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return bad("INVALID_VALUE", "“%s”应为 1–65535 的整数", f.Label)
		}
	case TSelect:
		if v == "" {
			return nil
		}
		for _, o := range f.Options {
			if o.Value == v {
				return nil
			}
		}
		return bad("INVALID_VALUE", "“%s”的取值无效", f.Label)
	}
	return nil
}

// CleanRel validates a provider-relative path. "" is the root. Backslashes, "..", empty or "." components and
// control characters are rejected; names may otherwise contain any character a remote store allows.
func CleanRel(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if len(p) > 1024 || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") {
		return "", bad("INVALID_PATH", "路径无效")
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return "", bad("INVALID_PATH", "路径含有控制字符")
		}
	}
	if path.Clean(p) != p {
		return "", bad("INVALID_PATH", "路径无效")
	}
	for _, c := range strings.Split(p, "/") {
		if c == "" || c == "." || c == ".." {
			return "", bad("INVALID_PATH", "路径无效")
		}
	}
	return p, nil
}

// Join joins the base path and a validated relative path.
func Join(base, rel string) string {
	switch {
	case base == "":
		return rel
	case rel == "":
		return base
	}
	return base + "/" + rel
}

// BlockedIP reports addresses a user-configured connection may never reach: link-local (including the cloud
// metadata service 169.254.169.254), unspecified and multicast. Private/LAN and loopback addresses are allowed:
// reaching internal storage is the point of this feature.
func BlockedIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// SafeDialer connects like net.Dialer but re-checks the resolved address at connect time (defeating DNS
// rebinding to the metadata address). Providers must use its DialContext for every outbound connection.
func SafeDialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip, err := netip.ParseAddr(host)
		if err != nil {
			return err
		}
		if BlockedIP(ip) {
			return fmt.Errorf("%w: address %s is not allowed", ErrUnreachable, ip)
		}
		return nil
	}}
}

// CheckHost validates a host name or IP typed by the user (no scheme, path or credentials).
func CheckHost(h string) error {
	h = strings.TrimSpace(h)
	if h == "" || len(h) > 253 || strings.ContainsAny(h, "/\\@?#% \t\r\n") {
		return bad("INVALID_HOST", "主机地址无效")
	}
	if ip, err := netip.ParseAddr(strings.Trim(h, "[]")); err == nil && BlockedIP(ip) {
		return bad("INVALID_HOST", "不允许使用该地址")
	}
	return nil
}

// Open validates nothing: callers run Validate first. It connects the provider for the connection.
func Open(ctx context.Context, kindID string, s Settings) (Provider, error) {
	k, ok := KindByID(kindID)
	if !ok || k.Factory == nil {
		return nil, bad("UNKNOWN_KIND", "不支持的存储类型")
	}
	return k.Factory(ctx, s)
}

// Step is one line of a connection test report.
type Step struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type Report struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Steps     []Step `json:"steps"`
}

// Describe turns a provider error into a short user-facing message that never contains credentials.
func Describe(err error) string {
	var hk *HostKeyError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &hk):
		if hk.Changed {
			return "服务器的 SSH 主机密钥与已信任的不一致（可能存在中间人攻击，或服务器被重装）"
		}
		return "首次连接：请核对并确认服务器的 SSH 主机密钥指纹"
	case errors.Is(err, ErrAuth):
		return "认证失败：请检查用户名、密码或密钥"
	case errors.Is(err, ErrPermission):
		return "权限不足：该账号没有此目录的访问权限"
	case errors.Is(err, ErrNotFound):
		return "路径不存在"
	case errors.Is(err, ErrExists):
		return "目标已存在，未覆盖"
	case errors.Is(err, ErrUnreachable):
		return "无法连接服务器：请检查地址、端口、防火墙与网络"
	case errors.Is(err, ErrUnsupported):
		return "该存储不支持此操作"
	case errors.Is(err, ErrTooLarge):
		return "文件过大"
	case errors.Is(err, context.DeadlineExceeded):
		return "连接超时"
	}
	return "操作失败"
}

// Probe runs the connection test: connect → list the root → (when needWrite) write and remove a marker file.
// The marker is a tiny uniquely named object, removed immediately; nothing else is ever deleted.
func Probe(ctx context.Context, p Provider, needWrite bool, marker string) Report {
	start := time.Now()
	r := Report{}
	add := func(name string, err error, okMsg string) bool {
		s := Step{Name: name, OK: err == nil, Message: okMsg}
		if err != nil {
			s.Message = Describe(err)
		}
		r.Steps = append(r.Steps, s)
		return err == nil
	}
	ents, err := p.List(ctx, "")
	if !add("读取目录", err, "") {
		r.LatencyMS = time.Since(start).Milliseconds()
		return r
	}
	r.Steps[len(r.Steps)-1].Message = fmt.Sprintf("可读取，根目录下 %d 项", len(ents))
	ok := true
	if needWrite {
		body := "oa-agent write test"
		err = p.PutNew(ctx, marker, strings.NewReader(body), int64(len(body)))
		ok = add("写入权限", err, "可写入")
		if err == nil {
			if rerr := p.Remove(ctx, marker); rerr != nil {
				r.Steps = append(r.Steps, Step{Name: "清理测试文件", OK: false, Message: "已写入测试文件 " + marker + "，但无法删除，请手动清理"})
			}
		}
	}
	r.OK = ok
	r.LatencyMS = time.Since(start).Milliseconds()
	return r
}

// SortEntries orders folders first, then by name (case-insensitive).
func SortEntries(es []Entry) {
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].IsDir != es[j].IsDir {
			return es[i].IsDir
		}
		return strings.ToLower(es[i].Name) < strings.ToLower(es[j].Name)
	})
}
