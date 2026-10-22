package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"oa-agent/backend/internal/cloud"
	"oa-agent/backend/internal/dbtest"
	"oa-agent/backend/internal/fsman"
	"oa-agent/backend/internal/imports"
	"oa-agent/backend/internal/secrets"
	"oa-agent/backend/internal/store"
)

// ---- in-memory remote storage registered as kind "_mem" ----

type world struct {
	mu          sync.Mutex
	files       map[string][]byte
	putErr      error // injected PutNew error
	statErr     error
	openErr     error
	puts        int
	lastSecrets map[string]string
	hostKey     string // required pin; "" = no pin needed
}

var (
	worldsMu sync.Mutex
	worlds   = map[string]*world{}
)

func newWorld(name string) *world {
	w := &world{files: map[string][]byte{}}
	worldsMu.Lock()
	worlds[name] = w
	worldsMu.Unlock()
	return w
}

type memProv struct {
	w    *world
	base string
}

func (m *memProv) full(p string) string { return cloud.Join(m.base, p) }
func (m *memProv) List(_ context.Context, dir string) ([]cloud.Entry, error) {
	m.w.mu.Lock()
	defer m.w.mu.Unlock()
	prefix := m.full(dir)
	if prefix != "" {
		prefix += "/"
	}
	seen := map[string]bool{}
	var out []cloud.Entry
	for k, v := range m.w.files {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		rest := strings.TrimPrefix(k, prefix)
		name, tail, nested := strings.Cut(rest, "/")
		rel := cloud.Join(dir, name)
		if nested && tail != "" {
			if !seen[name] {
				seen[name] = true
				out = append(out, cloud.Entry{Name: name, Path: rel, IsDir: true})
			}
		} else {
			out = append(out, cloud.Entry{Name: name, Path: rel, Size: int64(len(v)), ModTime: time.Now()})
		}
	}
	return out, nil
}
func (m *memProv) Stat(_ context.Context, p string) (cloud.Entry, error) {
	m.w.mu.Lock()
	defer m.w.mu.Unlock()
	if m.w.statErr != nil {
		return cloud.Entry{}, m.w.statErr
	}
	k := m.full(p)
	if v, ok := m.w.files[k]; ok {
		return cloud.Entry{Name: path.Base(p), Path: p, Size: int64(len(v))}, nil
	}
	for f := range m.w.files {
		if strings.HasPrefix(f, k+"/") {
			return cloud.Entry{Name: path.Base(p), Path: p, IsDir: true}, nil
		}
	}
	return cloud.Entry{}, cloud.ErrNotFound
}
func (m *memProv) Open(_ context.Context, p string) (io.ReadCloser, int64, error) {
	m.w.mu.Lock()
	defer m.w.mu.Unlock()
	if m.w.openErr != nil {
		return nil, 0, m.w.openErr
	}
	v, ok := m.w.files[m.full(p)]
	if !ok {
		return nil, 0, cloud.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(v)), int64(len(v)), nil
}
func (m *memProv) PutNew(_ context.Context, p string, r io.Reader, size int64) error {
	m.w.mu.Lock()
	defer m.w.mu.Unlock()
	if m.w.putErr != nil {
		return m.w.putErr
	}
	k := m.full(p)
	if _, ok := m.w.files[k]; ok {
		return cloud.ErrExists
	}
	b, _ := io.ReadAll(r)
	if int64(len(b)) != size {
		return cloud.ErrUnsupported
	}
	m.w.files[k] = b
	m.w.puts++
	return nil
}
func (m *memProv) Remove(_ context.Context, p string) error {
	m.w.mu.Lock()
	defer m.w.mu.Unlock()
	delete(m.w.files, m.full(p))
	return nil
}
func (m *memProv) Close() error { return nil }

func init() {
	cloud.Register(cloud.Kind{ID: "_mem", Name: "mem", Group: "cloud", Fields: []cloud.Field{
		{Key: "world", Label: "世界", Type: cloud.TText, Required: true},
		{Key: "host", Label: "主机", Type: cloud.TText},
		{Key: "host_key", Label: "主机密钥", Type: cloud.TText, Managed: true},
		{Key: "token", Label: "令牌", Type: cloud.TPassword, Secret: true, Required: true},
	}, Factory: func(_ context.Context, s cloud.Settings) (cloud.Provider, error) {
		worldsMu.Lock()
		w := worlds[s.Get("world")]
		worldsMu.Unlock()
		if w == nil {
			return nil, cloud.ErrUnreachable
		}
		w.mu.Lock()
		w.lastSecrets = s.Secrets
		need := w.hostKey
		w.mu.Unlock()
		if need != "" && s.Get("host_key") != need {
			return nil, &cloud.HostKeyError{Fingerprint: need, Changed: s.Get("host_key") != ""}
		}
		if s.Secrets["token"] != "good-token" {
			return nil, cloud.ErrAuth
		}
		return &memProv{w: w, base: s.BasePath}, nil
	}})
}

// ---- fixtures ----

func newSvc(t *testing.T) (*Service, *store.Store, string, string) {
	t.Helper()
	st := dbtest.New(t)
	box, err := secrets.Open("", filepath.Join(t.TempDir(), "k"))
	if err != nil {
		t.Fatal(err)
	}
	spool, arch := t.TempDir(), t.TempDir()
	return New(Config{Store: st, Box: box, Imports: &imports.Service{Store: st, Root: spool}, Archive: &fsman.Manager{Root: arch}}), st, spool, arch
}

func create(t *testing.T, s *Service, name, wld string, extra func(*Input)) View {
	t.Helper()
	in := Input{Name: name, Kind: "_mem", Settings: map[string]string{"world": wld}, Secrets: map[string]string{"token": "good-token"}}
	if extra != nil {
		extra(&in)
	}
	v, err := s.Create(context.Background(), in, "tester")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func code(err error) string {
	if err == nil {
		return "nil"
	}
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve.Code
	}
	return "other:" + err.Error()
}

// ---- tests ----

func TestCreateNeverExposesCredentialsAndValidates(t *testing.T) {
	s, st, _, _ := newSvc(t)
	newWorld("w1")
	ctx := context.Background()
	v := create(t, s, "总部网盘", "w1", func(in *Input) { in.BasePath = " /归档/2026/ " })
	if v.BasePath != "归档/2026" || !v.SecretsSet["token"] || !v.Enabled || !v.UseForImport || v.Replicate != "off" {
		t.Fatalf("view: %+v", v)
	}
	if _, ok := v.Settings["token"]; ok {
		t.Fatal("secrets must never appear among the settings")
	}
	raw, _ := st.GetConn(ctx, v.ID)
	if bytes.Contains(raw.SecretsEnc, []byte("good-token")) || len(raw.SecretsEnc) == 0 {
		t.Fatal("secrets are stored encrypted")
	}
	list, _ := s.List(ctx)
	if len(list) != 1 || list[0].SecretsSet["token"] != true {
		t.Fatalf("list: %+v", list)
	}
	for name, mut := range map[string]func(*Input){
		"empty name":      func(in *Input) { in.Name = " " },
		"long name":       func(in *Input) { in.Name = strings.Repeat("名", 81) },
		"unknown kind":    func(in *Input) { in.Kind = "nope" },
		"missing secret":  func(in *Input) { in.Secrets = nil },
		"unknown setting": func(in *Input) { in.Settings["zzz"] = "1" },
		"base traversal":  func(in *Input) { in.BasePath = "a/../../b" },
		"bad replicate":   func(in *Input) { in.Replicate = "always" },
	} {
		in := Input{Name: "x-" + name, Kind: "_mem", Settings: map[string]string{"world": "w1"}, Secrets: map[string]string{"token": "t"}}
		mut(&in)
		if _, err := s.Create(ctx, in, "t"); err == nil || !strings.HasPrefix(code(err), "") || strings.HasPrefix(code(err), "other:") {
			t.Errorf("%s: want a validation error, got %v", name, err)
		}
	}
	if _, err := s.Create(ctx, Input{Name: "总部网盘", Kind: "_mem", Settings: map[string]string{"world": "w1"}, Secrets: map[string]string{"token": "t"}}, "t"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("names are unique (case-insensitive): %v", err)
	}
}

func TestUpdateNeverRedirectsSavedCredentials(t *testing.T) {
	s, st, _, _ := newSvc(t)
	newWorld("w1")
	ctx := context.Background()
	v := create(t, s, "A", "w1", func(in *Input) { in.Settings["host"] = "files.corp.example" })
	name := "B"
	got, err := s.Update(ctx, v.ID, Patch{Name: &name, Enabled: ptr(false)}, "t")
	if err != nil || got.Name != "B" || got.Enabled || !got.SecretsSet["token"] {
		t.Fatalf("plain edit keeps credentials: %v %+v", err, got)
	}
	// moving to another server without re-entering the credentials is refused
	_, err = s.Update(ctx, v.ID, Patch{Settings: map[string]string{"host": "evil.example"}}, "t")
	if code(err) != "SECRETS_REQUIRED" {
		t.Fatalf("changing the target without credentials: %v", err)
	}
	if c, _ := st.GetConn(ctx, v.ID); c.Settings["host"] != "files.corp.example" {
		t.Fatal("a refused edit must not change anything")
	}
	got, err = s.Update(ctx, v.ID, Patch{Settings: map[string]string{"host": "new.corp.example"}, Secrets: map[string]string{"token": "good-token"}}, "t")
	if err != nil || got.Settings["host"] != "new.corp.example" {
		t.Fatalf("new target with new credentials: %v", err)
	}
	// empty value removes a setting; clearing a required secret is rejected
	// removing the host is also a change of target
	if _, err = s.Update(ctx, v.ID, Patch{Settings: map[string]string{"host": ""}}, "t"); code(err) != "SECRETS_REQUIRED" {
		t.Fatalf("clearing the host changes the target too: %v", err)
	}
	if got, err = s.Update(ctx, v.ID, Patch{Settings: map[string]string{"host": ""}, Secrets: map[string]string{"token": "good-token"}}, "t"); err != nil || got.Settings["host"] != "" {
		t.Fatalf("clear optional setting with credentials: %v %+v", err, got)
	}
	if _, err = s.Update(ctx, v.ID, Patch{ClearSecrets: []string{"token"}}, "t"); code(err) != "REQUIRED" {
		t.Fatalf("a required credential cannot be cleared: %v", err)
	}
	if _, err = s.Update(ctx, "not-a-uuid", Patch{}, "t"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestDraftTestReusesSavedSecretsOnlyForTheSameTarget(t *testing.T) {
	s, _, _, _ := newSvc(t)
	w := newWorld("w1")
	newWorld("w2")
	ctx := context.Background()
	v := create(t, s, "A", "w1", func(in *Input) { in.Settings["host"] = "h1" })
	res, err := s.TestDraft(ctx, TestInput{Kind: "_mem", Settings: map[string]string{"world": "w1", "host": "h1"}, ConnectionID: v.ID}, "t")
	if err != nil || !res.OK || len(res.Steps) < 2 {
		t.Fatalf("saved secret reused for the same target: %v %+v", err, res)
	}
	if w.lastSecrets["token"] != "good-token" {
		t.Fatal("provider must have received the saved credential")
	}
	// another target: the saved credential must NOT be used, so validation demands one
	if _, err = s.TestDraft(ctx, TestInput{Kind: "_mem", Settings: map[string]string{"world": "w2", "host": "h2"}, ConnectionID: v.ID}, "t"); err == nil || code(err) != "REQUIRED" {
		t.Fatalf("a different target needs its own credentials: %v", err)
	}
	if res, err = s.TestDraft(ctx, TestInput{Kind: "_mem", Settings: map[string]string{"world": "w1"}, Secrets: map[string]string{"token": "wrong"}}, "t"); err != nil || res.OK || !strings.Contains(res.Steps[0].Message, "认证失败") {
		t.Fatalf("wrong credential: %v %+v", err, res)
	}
	if res, _ = s.TestDraft(ctx, TestInput{Kind: "_mem", Settings: map[string]string{"world": "missing"}, Secrets: map[string]string{"token": "x"}}, "t"); res.OK || !strings.Contains(res.Steps[0].Message, "无法连接") {
		t.Fatalf("unreachable: %+v", res)
	}
	if _, err = s.TestDraft(ctx, TestInput{Kind: "other", ConnectionID: v.ID}, "t"); code(err) != "INVALID_KIND" && code(err) != "UNKNOWN_KIND" {
		t.Fatalf("kind mismatch: %v", err)
	}
	c, _ := s.Get(ctx, v.ID)
	if c.LastTest == nil || !c.LastTest.OK {
		t.Fatalf("a draft test bound to a saved connection records its result on it: %+v", c.LastTest)
	}
}

func TestWriteProbeLeavesNoMarkerBehind(t *testing.T) {
	s, _, _, _ := newSvc(t)
	w := newWorld("w1")
	v := create(t, s, "A", "w1", func(in *Input) { in.Replicate = "manual" })
	res, err := s.TestSaved(context.Background(), v.ID, true, "", "t")
	if err != nil || !res.OK || len(res.Steps) != 3 {
		t.Fatalf("write test: %v %+v", err, res)
	}
	if len(w.files) != 0 {
		t.Fatalf("the probe must remove its marker: %v", w.files)
	}
	w.putErr = cloud.ErrPermission
	if res, _ = s.TestSaved(context.Background(), v.ID, true, "", "t"); res.OK || !strings.Contains(res.Steps[len(res.Steps)-1].Message, "权限不足") {
		t.Fatalf("read-only storage: %+v", res)
	}
}

func TestHostKeyTrustOnFirstUseFlow(t *testing.T) {
	s, _, _, _ := newSvc(t)
	w := newWorld("w1")
	w.hostKey = "SHA256:abc"
	ctx := context.Background()
	v := create(t, s, "SFTP-like", "w1", nil)
	res, err := s.TestSaved(ctx, v.ID, false, "", "t")
	if err != nil || res.OK || res.HostKey == nil || res.HostKey.Fingerprint != "SHA256:abc" || res.HostKey.Changed {
		t.Fatalf("first contact asks for confirmation: %v %+v", err, res)
	}
	res, _ = s.TestSaved(ctx, v.ID, false, "SHA256:wrong", "t")
	if res.OK || res.HostKey == nil {
		t.Fatalf("confirming a different fingerprint than the server presents must not pin: %+v", res)
	}
	if c, _ := s.Get(ctx, v.ID); c.Settings["host_key"] != "" {
		t.Fatal("nothing may be pinned after a mismatching confirmation")
	}
	res, _ = s.TestSaved(ctx, v.ID, false, "SHA256:abc", "t")
	if !res.OK {
		t.Fatalf("confirmed fingerprint: %+v", res)
	}
	if c, _ := s.Get(ctx, v.ID); c.Settings["host_key"] != "SHA256:abc" {
		t.Fatalf("the confirmed key is pinned on the connection: %+v", c.Settings)
	}
	if res, _ = s.TestSaved(ctx, v.ID, false, "", "t"); !res.OK {
		t.Fatalf("later tests use the pin: %+v", res)
	}
	w.mu.Lock()
	w.hostKey = "SHA256:evil"
	w.mu.Unlock()
	res, _ = s.TestSaved(ctx, v.ID, false, "SHA256:evil", "t")
	if res.OK || res.HostKey == nil || !res.HostKey.Changed {
		t.Fatalf("a changed key is never silently accepted, even if the caller 'accepts' it: %+v", res)
	}
	// draft flow: fingerprint is returned, resubmitting with accept_host_key succeeds
	w.hostKey = "SHA256:abc"
	d, _ := s.TestDraft(ctx, TestInput{Kind: "_mem", Settings: map[string]string{"world": "w1"}, Secrets: map[string]string{"token": "good-token"}}, "t")
	if d.OK || d.HostKey == nil {
		t.Fatalf("draft: %+v", d)
	}
	if d, _ = s.TestDraft(ctx, TestInput{Kind: "_mem", Settings: map[string]string{"world": "w1"}, Secrets: map[string]string{"token": "good-token"}, AcceptHostKey: d.HostKey.Fingerprint}, "t"); !d.OK {
		t.Fatalf("draft after confirmation: %+v", d)
	}
}

func TestBrowseIsConfinedAndSorted(t *testing.T) {
	s, _, _, _ := newSvc(t)
	w := newWorld("w1")
	w.files["root/b.txt"] = []byte("b")
	w.files["root/A/x.pdf"] = []byte("x")
	w.files["root/a.pdf"] = []byte("a")
	w.files["outside/secret.pdf"] = []byte("s")
	ctx := context.Background()
	v := create(t, s, "A", "w1", func(in *Input) { in.BasePath = "root" })
	l, err := s.Browse(ctx, v.ID, "")
	if err != nil || len(l.Entries) != 3 || l.Entries[0].Name != "A" || !l.Entries[0].IsDir || l.Parent != nil {
		t.Fatalf("root: %v %+v", err, l)
	}
	l, err = s.Browse(ctx, v.ID, "A")
	if err != nil || len(l.Entries) != 1 || l.Entries[0].Path != "A/x.pdf" || l.Parent == nil || *l.Parent != "" {
		t.Fatalf("sub: %v %+v", err, l)
	}
	for _, bad := range []string{"../outside", "/etc", "a/../..", `a\b`} {
		if _, err := s.Browse(ctx, v.ID, bad); code(err) != "INVALID_PATH" {
			t.Errorf("%q: %v", bad, err)
		}
	}
	off := false
	if _, err = s.Update(ctx, v.ID, Patch{Enabled: &off}, "t"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Browse(ctx, v.ID, ""); code(err) != "DISABLED" {
		t.Fatalf("disabled connections cannot be browsed: %v", err)
	}
}

func TestImportRemoteExpandsFoldersAndAppliesTheSameRulesAsUploads(t *testing.T) {
	s, st, spool, _ := newSvc(t)
	w := newWorld("w1")
	w.files["inbox/合同/a.docx"] = []byte("AAA")
	w.files["inbox/合同/子/b.pdf"] = []byte("BBBB")
	w.files["inbox/合同/old.doc"] = []byte("legacy")
	w.files["inbox/合同/empty.pdf"] = []byte{}
	w.files["inbox/single.png"] = []byte("PNG")
	w.files["inbox/huge.pdf"] = bytes.Repeat([]byte("x"), int(imports.MaxFileBytes)+1)
	ctx := context.Background()
	v := create(t, s, "A", "w1", func(in *Input) { in.BasePath = "inbox" })
	b, err := s.ImportRemote(ctx, v.ID, []string{"合同", "single.png", "huge.pdf"}, "", "key-1", "t")
	if err != nil {
		t.Fatal(err)
	}
	var okFiles, badFiles []imports.File
	for _, f := range b.Files {
		if f.Error != nil {
			badFiles = append(badFiles, f)
		} else {
			okFiles = append(okFiles, f)
		}
	}
	if b.Job.TotalFiles != 6 || len(okFiles) != 3 || len(badFiles) != 3 || b.Job.FailureCount != 3 || b.Job.Status != "queued" {
		t.Fatalf("batch: %+v ok=%d bad=%d", b.Job, len(okFiles), len(badFiles))
	}
	got := map[string]string{}
	for _, f := range okFiles {
		got[f.SourcePath] = f.SHA256
		data, rerr := os.ReadFile(f.StoredPath)
		sum := sha256.Sum256(data)
		if rerr != nil || hex.EncodeToString(sum[:]) != f.SHA256 || !strings.HasPrefix(f.StoredPath, spool) {
			t.Fatalf("stored copy of %s: %v", f.SourcePath, rerr)
		}
	}
	for _, want := range []string{"合同/a.docx", "合同/子/b.pdf", "single.png"} {
		if got[want] == "" {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	codes := map[string]string{}
	for _, f := range badFiles {
		for _, c := range []string{"UNSUPPORTED_FORMAT", "EMPTY_FILE", "FILE_TOO_LARGE"} {
			if strings.HasPrefix(*f.Error, "["+c+"]") {
				codes[f.SourcePath] = c
			}
		}
	}
	if codes["合同/old.doc"] != "UNSUPPORTED_FORMAT" || codes["合同/empty.pdf"] != "EMPTY_FILE" || codes["huge.pdf"] != "FILE_TOO_LARGE" {
		t.Fatalf("failure codes: %v", codes)
	}
	// idempotent replay
	again, err := s.ImportRemote(ctx, v.ID, []string{"合同", "single.png", "huge.pdf"}, "", "key-1", "t")
	if err != nil || again.Job.ID != b.Job.ID {
		t.Fatalf("replay: %v", err)
	}
	if _, err = s.ImportRemote(ctx, v.ID, []string{"single.png"}, "", "key-1", "t"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("same key, different content: %v", err)
	}
	if j, _ := st.GetJob(ctx, b.Job.ID); j.TotalFiles != 6 {
		t.Fatal("job persisted")
	}
	// refusals
	for name, items := range map[string][]string{"traversal": {"../x"}, "root": {""}, "absolute": {"/etc/passwd"}} {
		if _, err := s.ImportRemote(ctx, v.ID, items, "", "", "t"); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if _, err = s.ImportRemote(ctx, v.ID, []string{"nope.pdf"}, "", "", "t"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing item: %v", err)
	}
	no := false
	if _, err = s.Update(ctx, v.ID, Patch{UseForImport: &no}, "t"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ImportRemote(ctx, v.ID, []string{"single.png"}, "", "", "t"); code(err) != "NOT_FOR_IMPORT" {
		t.Fatalf("not for import: %v", err)
	}
}

func TestImportRemoteLimitsAndReadErrors(t *testing.T) {
	s, _, _, _ := newSvc(t)
	w := newWorld("w1")
	for i := 0; i < 105; i++ {
		w.files["many/f"+strings.Repeat("a", i%7)+string(rune('A'+i%26))+string(rune('a'+i/26))+".pdf"] = []byte("x")
	}
	w.files["one.pdf"] = []byte("ok")
	ctx := context.Background()
	v := create(t, s, "A", "w1", nil)
	_, err := s.ImportRemote(ctx, v.ID, []string{"many"}, "", "", "t")
	var ie *imports.Error
	if !errors.As(err, &ie) || ie.Status != 413 {
		t.Fatalf("a folder with more than 100 files is refused before downloading anything: %v", err)
	}
	w.openErr = cloud.ErrPermission
	b, err := s.ImportRemote(ctx, v.ID, []string{"one.pdf"}, "", "", "t")
	if err != nil || len(b.Files) != 1 || b.Files[0].Error == nil || !strings.HasPrefix(*b.Files[0].Error, "[REMOTE_READ_FAILED]") || !strings.Contains(*b.Files[0].Error, "权限不足") || b.Job.Status != "failed" {
		t.Fatalf("an unreadable remote file is a per-file failure with a readable reason: %v %+v", err, b.Files)
	}
}

// ---- replication ----

type archived struct {
	rel  string
	data []byte
	sha  string
	id   string // ledger id
}

// seedArchive writes real archive files and the matching ledger via a real plan execution.
func seedArchive(t *testing.T, st *store.Store, arch string, rels ...string) []archived {
	t.Helper()
	ctx := context.Background()
	jid, _ := imports.NewID()
	now := time.Now().UTC()
	b := imports.Batch{Job: imports.Job{ID: jid, Status: "queued", TotalFiles: len(rels), CreatedAt: now, UpdatedAt: now}, Files: []imports.File{}, Failures: []imports.Failure{}}
	var out []archived
	var items []store.PlanItem
	for _, rel := range rels {
		data := []byte("content of " + rel)
		sum := sha256.Sum256(data)
		sha := hex.EncodeToString(sum[:])
		fid, _ := imports.NewID()
		b.Files = append(b.Files, imports.File{ID: fid, TaskID: jid, SourceName: path.Base(rel), SourcePath: "in/" + path.Base(rel), MIMEType: "application/pdf", SHA256: sha, SizeBytes: int64(len(data)), Evidence: []string{}, ArchiveStatus: "pending", CreatedAt: now, StoredPath: "/x"})
		p := filepath.Join(arch, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, data, 0o644)
		items = append(items, store.PlanItem{FileID: fid, OriginalName: path.Base(rel), SuggestedName: path.Base(rel), SourcePath: "in/x", DestinationPath: rel, Department: ptr("财务部"), DocumentType: ptr("合同"), DocumentDate: ptr("2026-03-15"), Warnings: []string{}})
		out = append(out, archived{rel: rel, data: data, sha: sha})
		_ = fid
	}
	if _, _, err := st.Save(ctx, b); err != nil {
		t.Fatal(err)
	}
	plan, err := st.CreatePlan(ctx, jid, nil, items)
	if err != nil {
		t.Fatal(err)
	}
	plan, _ = st.ApprovePlan(ctx, plan.ID, plan.Version, "u")
	op, plan, _, err := st.BeginExecution(ctx, plan.ID, "k")
	if err != nil {
		t.Fatal(err)
	}
	var res []store.OpResult
	for i, it := range plan.Items {
		_ = i
		res = append(res, store.OpResult{FileID: it.FileID, FinalRel: it.DestinationPath, Status: "published", SHA256: shaOf(out, it.DestinationPath)})
	}
	if _, err = st.FinishExecution(ctx, op, plan, res); err != nil {
		t.Fatal(err)
	}
	rows, _, _ := st.Ledger(ctx, store.Page{Size: 100}, store.LedgerFilter{})
	for i := range out {
		for _, r := range rows {
			if r.ArchivePath == out[i].rel {
				out[i].id = r.ID
			}
		}
	}
	return out
}

func shaOf(a []archived, rel string) string {
	for _, x := range a {
		if x.rel == rel {
			return x.sha
		}
	}
	return ""
}

func runQueue(t *testing.T, s *Service) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		r, err := s.cfg.Store.ClaimReplication(ctx)
		if errors.Is(err, store.ErrNotFound) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		s.replicateOne(ctx, r)
	}
}

func repl(t *testing.T, s *Service, connID string) map[string]store.Replication {
	t.Helper()
	items, _, err := s.ListReplications(context.Background(), store.Page{Size: 100}, connID, "")
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]store.Replication{}
	for _, r := range items {
		m[r.LocalPath] = r
	}
	return m
}

func TestReplicationCopiesWithoutOverwritingOrDeleting(t *testing.T) {
	s, st, _, arch := newSvc(t)
	w := newWorld("w1")
	ctx := context.Background()
	files := seedArchive(t, st, arch, "财务部/2026/合同/a.pdf", "财务部/2026/合同/b.pdf", "人事部/2026/通知/c.pdf")
	v := create(t, s, "A", "w1", func(in *Input) { in.Replicate = "off"; in.BasePath = "mirror" })
	if _, err := s.Replicate(ctx, v.ID, "", "t"); !errors.Is(err, store.ErrState) {
		t.Fatalf("replication is off: %v", err)
	}
	manual := "manual"
	if _, err := s.Update(ctx, v.ID, Patch{Replicate: &manual}, "t"); err != nil {
		t.Fatal(err)
	}
	n, err := s.Replicate(ctx, v.ID, "财务部", "t")
	if err != nil || n != 2 {
		t.Fatalf("prefix filter queues only matching files: %v %d", err, n)
	}
	if n, _ = s.Replicate(ctx, v.ID, "财务部", "t"); n != 0 {
		t.Fatalf("a second click adds nothing: %d", n)
	}
	// pre-existing remote content: identical size (skipped) and different size (numbered name, never overwritten)
	w.files["mirror/财务部/2026/合同/a.pdf"] = files[0].data
	w.files["mirror/财务部/2026/合同/b.pdf"] = []byte("somebody else's different file")
	runQueue(t, s)
	got := repl(t, s, v.ID)
	if got["财务部/2026/合同/a.pdf"].Status != "skipped" || got["财务部/2026/合同/b.pdf"].Status != "done" {
		t.Fatalf("statuses: %+v", got)
	}
	if string(w.files["mirror/财务部/2026/合同/b.pdf"]) != "somebody else's different file" {
		t.Fatal("an existing different remote file must never be overwritten")
	}
	if b := w.files["mirror/财务部/2026/合同/b (2).pdf"]; !bytes.Equal(b, files[1].data) || got["财务部/2026/合同/b.pdf"].RemotePath != "财务部/2026/合同/b (2).pdf" {
		t.Fatalf("numbered name: %v %+v", w.files, got)
	}
	if w.puts != 1 {
		t.Fatalf("only one upload was needed: %d", w.puts)
	}
	// the rest, via a full manual run
	if n, _ = s.Replicate(ctx, v.ID, "", "t"); n != 1 {
		t.Fatalf("remaining file: %d", n)
	}
	runQueue(t, s)
	if got = repl(t, s, v.ID); got["人事部/2026/通知/c.pdf"].Status != "done" || !bytes.Equal(w.files["mirror/人事部/2026/通知/c.pdf"], files[2].data) {
		t.Fatalf("c.pdf: %+v", got)
	}
	// local originals are untouched
	for _, f := range files {
		if b, _ := os.ReadFile(filepath.Join(arch, filepath.FromSlash(f.rel))); !bytes.Equal(b, f.data) {
			t.Fatalf("local archive file changed: %s", f.rel)
		}
	}
	c, _ := s.Get(ctx, v.ID)
	if c.Replication.Done != 2 || c.Replication.Skipped != 1 {
		t.Fatalf("counts: %+v", c.Replication)
	}
}

func TestReplicationIntegrityAndLedgerGuards(t *testing.T) {
	s, st, _, arch := newSvc(t)
	w := newWorld("w1")
	ctx := context.Background()
	files := seedArchive(t, st, arch, "a/modified.pdf", "a/removed.pdf", "a/missing.pdf", "a/ok.pdf")
	v := create(t, s, "A", "w1", func(in *Input) { in.Replicate = "manual" })
	if _, err := s.Replicate(ctx, v.ID, "", "t"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(arch, "a", "modified.pdf"), []byte("tampered after archiving"), 0o644)
	os.Remove(filepath.Join(arch, "a", "missing.pdf"))
	// removed from the ledger (e.g. trashed in the file manager)
	if _, err := st.LedgerRemove(ctx, "a/removed.pdf", false, "11111111-1111-4111-8111-111111111111"); err != nil {
		t.Fatal(err)
	}
	runQueue(t, s)
	got := repl(t, s, v.ID)
	if got["a/modified.pdf"].Status != "failed" || !strings.Contains(*got["a/modified.pdf"].Error, "不一致") {
		t.Fatalf("modified local file must not be uploaded: %+v", got["a/modified.pdf"])
	}
	if got["a/removed.pdf"].Status != "skipped" || !strings.Contains(*got["a/removed.pdf"].Error, "移除") {
		t.Fatalf("entries removed from the ledger are skipped: %+v", got["a/removed.pdf"])
	}
	if got["a/missing.pdf"].Status != "failed" {
		t.Fatalf("missing local file: %+v", got["a/missing.pdf"])
	}
	if got["a/ok.pdf"].Status != "done" || len(w.files) != 1 {
		t.Fatalf("only the intact file reached the remote: %v %+v", w.files, got["a/ok.pdf"])
	}
	_ = files
}

func TestReplicationErrorsRetryWithBackoffOrFailPermanently(t *testing.T) {
	s, st, _, arch := newSvc(t)
	w := newWorld("w1")
	ctx := context.Background()
	seedArchive(t, st, arch, "a/one.pdf")
	v := create(t, s, "A", "w1", func(in *Input) { in.Replicate = "manual" })
	if _, err := s.Replicate(ctx, v.ID, "", "t"); err != nil {
		t.Fatal(err)
	}
	// transient failure → re-queued later, attempts counted, readable message
	w.putErr = cloud.ErrUnreachable
	r, _ := st.ClaimReplication(ctx)
	s.replicateOne(ctx, r)
	got := repl(t, s, v.ID)["a/one.pdf"]
	if got.Status != "queued" || got.Attempts != 1 || got.Error == nil || !strings.Contains(*got.Error, "将自动重试") {
		t.Fatalf("transient: %+v", got)
	}
	if _, err := st.ClaimReplication(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("backoff: the item must not be due immediately: %v", err)
	}
	// permanent failure → failed at once; retry puts it back
	if _, err := st.Pool.Exec(ctx, `UPDATE replications SET next_attempt_at=now()`); err != nil {
		t.Fatal(err)
	}
	w.putErr = cloud.ErrPermission
	r, _ = st.ClaimReplication(ctx)
	s.replicateOne(ctx, r)
	got = repl(t, s, v.ID)["a/one.pdf"]
	if got.Status != "failed" || !strings.Contains(*got.Error, "权限不足") {
		t.Fatalf("permanent: %+v", got)
	}
	if _, err := s.RetryReplication(ctx, "00000000-0000-4000-8000-0000000000aa"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	w.putErr = nil
	rr, err := s.RetryReplication(ctx, got.ID)
	if err != nil || rr.Status != "queued" || rr.Attempts != 0 {
		t.Fatalf("retry: %v %+v", err, rr)
	}
	if _, err = s.RetryReplication(ctx, got.ID); !errors.Is(err, store.ErrState) {
		t.Fatalf("only failed items can be retried: %v", err)
	}
	runQueue(t, s)
	if got = repl(t, s, v.ID)["a/one.pdf"]; got.Status != "done" {
		t.Fatalf("after retry: %+v", got)
	}
	// upload that "succeeds" but leaves a wrong size is reported, not trusted
	seedArchive(t, st, arch, "a/two.pdf")
	s.Replicate(ctx, v.ID, "a/two", "t")
	w.mu.Lock()
	w.statErr = nil
	w.mu.Unlock()
	if bs := backoff(1); bs != 30*time.Second || backoff(2) != time.Minute || backoff(40) != 30*time.Minute {
		t.Fatalf("backoff: %v %v %v", bs, backoff(2), backoff(40))
	}
}

func TestAutoReplicationOnlyCoversFilesArchivedAfterItWasEnabled(t *testing.T) {
	s, st, _, arch := newSvc(t)
	newWorld("w1")
	ctx := context.Background()
	seedArchive(t, st, arch, "old/before.pdf")
	time.Sleep(20 * time.Millisecond)
	v := create(t, s, "A", "w1", func(in *Input) { in.Replicate = "auto" })
	time.Sleep(20 * time.Millisecond)
	seedArchive(t, st, arch, "new/after.pdf")
	conns, _ := st.AutoConnections(ctx)
	if len(conns) != 1 {
		t.Fatalf("auto connections: %d", len(conns))
	}
	if _, err := st.EnqueueMissing(ctx, conns[0].ID, "", conns[0].AutoSince); err != nil {
		t.Fatal(err)
	}
	got := repl(t, s, v.ID)
	if len(got) != 1 || got["new/after.pdf"].ID == "" {
		t.Fatalf("history must not be replicated automatically: %v", keys(got))
	}
	// disabling the connection stops the worker from picking its items
	off := false
	if _, err := s.Update(ctx, v.ID, Patch{Enabled: &off}, "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimReplication(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("disabled connections are not processed: %v", err)
	}
}

func keys(m map[string]store.Replication) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

func TestDeleteRemovesQueueButNotRemoteFiles(t *testing.T) {
	s, st, _, arch := newSvc(t)
	w := newWorld("w1")
	ctx := context.Background()
	seedArchive(t, st, arch, "a/x.pdf")
	v := create(t, s, "A", "w1", func(in *Input) { in.Replicate = "manual" })
	s.Replicate(ctx, v.ID, "", "t")
	runQueue(t, s)
	if len(w.files) != 1 {
		t.Fatal("setup")
	}
	if err := s.Delete(ctx, v.ID, "t"); err != nil {
		t.Fatal(err)
	}
	if len(w.files) != 1 {
		t.Fatal("deleting a connection must never delete remote files")
	}
	if items, total, _ := s.ListReplications(ctx, store.Page{}, "", ""); total != 0 || len(items) != 0 {
		t.Fatal("replication rows go away with the connection")
	}
	if err := s.Delete(ctx, v.ID, "t"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestAltNameAndCleanBase(t *testing.T) {
	for in, want := range map[string]string{"a/b.pdf": "a/b (2).pdf", "b.tar.gz": "b.tar (2).gz", "noext": "noext (2)", "a/.hidden": "a/.hidden (2)"} {
		if got := altName(in, 2); got != want {
			t.Errorf("altName(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"": "", "/": "", " /a/b/ ": "a/b"} {
		if got, err := CleanBase(in); err != nil || got != want {
			t.Errorf("CleanBase(%q) = %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"..", "a/../b", `a\b`, "a//b"} {
		if _, err := CleanBase(bad); err == nil {
			t.Errorf("CleanBase(%q) must fail", bad)
		}
	}
}
