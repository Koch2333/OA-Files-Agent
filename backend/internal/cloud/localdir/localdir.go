// Package localdir is the "local / mounted directory" storage provider. On a Linux server the operator bind-mounts
// NAS/NFS/disk directories into the container (e.g. /mnt/storage/nas1) and lists the allowed mount roots in the
// server environment OA_LOCAL_ROOTS. A connection's `path` must resolve (after symlinks) inside one of those roots.
//
// Security model:
//   - Allowed roots come only from OA_LOCAL_ROOTS, never from user input. Empty = every connection fails.
//   - The resolved path may not overlap (either direction) the application's archive root, spool root or data dir.
//   - All file access goes through os.Root (openat-style, kernel-confined to the connection directory, a symlink
//     can never lead out of it, ".." is refused) and, on top of that, every path component is Lstat-checked and any
//     symlink is refused. Hidden names (leading ".") are unreachable, except the connection-test marker.
//   - Opened files are re-checked with fstat (must be regular) so a swap after the Lstat cannot expose a device/FIFO.
//   - PutNew writes a hidden temp file in the target directory and publishes it with link(2) (fails with EEXIST,
//     never replaces), then removes the temp name. Files are 0640, directories 0750.
//
// Residual risk: between the per-component Lstat and the open a component may be swapped for a symlink that points
// to somewhere INSIDE the connection directory (os.Root still confines it; it cannot leave the directory). The
// connection directory itself is resolved once at connect time; replacing the mount point underneath a live
// connection is outside this package's control. Mount NAS shares with nosuid,nodev and, for import-only
// mounts, read-only (plus read_only=true here).
package localdir

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"oa-agent/backend/internal/cloud"
)

const (
	maxEntries    = 5000
	tmpPrefix     = ".oa-agent-tmp-"
	markerPrefix  = ".oa-agent-write-test-"
	envRoots      = "OA_LOCAL_ROOTS"
	notConfigured = "服务器未配置可用的本地挂载目录（OA_LOCAL_ROOTS）"
)

func init() {
	cloud.Register(cloud.Kind{
		ID:          "localdir",
		Name:        "本地目录 / 挂载目录",
		Group:       "enterprise",
		Description: "使用服务器上已挂载的目录（NAS/NFS/磁盘）。需要管理员先把目录挂载进服务并在环境变量 OA_LOCAL_ROOTS 中列出允许的根目录；只能访问这些目录之内的内容。",
		Fields: []cloud.Field{
			{Key: "path", Label: "目录路径", Type: cloud.TText, Required: true, Placeholder: "/mnt/storage/nas1",
				Help: "服务器上的绝对路径，必须位于管理员允许的挂载根目录之内（OA_LOCAL_ROOTS），且不能与本系统的归档/数据目录重叠。"},
			{Key: "read_only", Label: "只读", Type: cloud.TSelect, Default: "false", Options: []cloud.Option{{Value: "false", Label: "否（可导入也可作为同步目标）"}, {Value: "true", Label: "是（仅用于导入，禁止写入）"}},
				Help: "只读时拒绝一切写入与删除。"},
		},
		Factory: factory,
	})
}

func invalid(msg string) error { return &cloud.ValidationError{Code: "INVALID_VALUE", Msg: msg} }

func fold(p string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}

// within reports whether p equals root or lies below it (both cleaned absolute paths).
func within(root, p string) bool {
	root, p = fold(root), fold(p)
	if p == root {
		return true
	}
	r, err := filepath.Rel(root, p)
	return err == nil && r != "." && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}

// resolveLoose resolves symlinks of an existing path, or, for a path that does not exist, of its nearest existing
// ancestor plus the remainder.
func resolveLoose(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(resolveLoose(parent), filepath.Base(p))
}

// AllowedRoots returns the resolved absolute roots from OA_LOCAL_ROOTS (invalid entries are ignored).
func AllowedRoots() []string {
	var out []string
	for _, r := range filepath.SplitList(os.Getenv(envRoots)) {
		r = strings.TrimSpace(r)
		if r == "" || !filepath.IsAbs(r) {
			continue
		}
		rr, err := filepath.EvalSymlinks(filepath.Clean(r))
		if err != nil {
			continue
		}
		if fi, err := os.Stat(rr); err != nil || !fi.IsDir() {
			continue
		}
		out = append(out, rr)
	}
	return out
}

// appDirs lists the directories of the application itself that a connection may never touch.
func appDirs() []string {
	var out []string
	add := func(p string) {
		if p = strings.TrimSpace(p); p != "" {
			if a, err := filepath.Abs(p); err == nil {
				out = append(out, resolveLoose(a))
			}
		}
	}
	add(os.Getenv("OA_ARCHIVE_ROOT"))
	if sp := strings.TrimSpace(os.Getenv("OA_SPOOL_ROOT")); sp != "" {
		add(sp)
		if a, err := filepath.Abs(sp); err == nil {
			add(filepath.Dir(a)) // data dir (holds secret.key and the other app data)
		}
	}
	return out
}

// checkRel validates a relative path for use below the connection directory and returns its components.
func checkRel(p string, allowMarker bool) ([]string, error) {
	p, err := cloud.CleanRel(p)
	if err != nil {
		return nil, err
	}
	if p == "" {
		return nil, nil
	}
	parts := strings.Split(p, "/")
	for i, c := range parts {
		if strings.HasPrefix(c, ".") {
			if allowMarker && i == len(parts)-1 && strings.HasPrefix(c, markerPrefix) {
				continue
			}
			return nil, fmt.Errorf("%w: hidden names are not accessible", cloud.ErrPermission)
		}
	}
	return parts, nil
}

type provider struct {
	root     *os.Root
	prefix   []string // Settings.BasePath components below the connection directory
	readOnly bool
}

func factory(_ context.Context, s cloud.Settings) (cloud.Provider, error) {
	roots := AllowedRoots()
	if len(roots) == 0 {
		return nil, invalid(notConfigured)
	}
	raw := s.Get("path")
	if raw == "" {
		return nil, &cloud.ValidationError{Code: "REQUIRED", Msg: "请填写“目录路径”"}
	}
	if strings.ContainsRune(raw, 0) || !filepath.IsAbs(raw) {
		return nil, invalid("目录路径必须是服务器上的绝对路径，例如 /mnt/storage/nas1")
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(raw))
	if err != nil {
		return nil, invalid("目录不存在或无法访问：请确认已挂载到服务并路径正确")
	}
	real = filepath.Clean(real)
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return nil, invalid("该路径不是目录")
	}
	ok := false
	for _, r := range roots {
		if within(r, real) {
			ok = true
			break
		}
	}
	if !ok {
		return nil, invalid("该目录不在管理员允许的挂载根目录之内（OA_LOCAL_ROOTS）")
	}
	for _, d := range appDirs() {
		if within(d, real) || within(real, d) {
			return nil, invalid("该目录与本系统自身的归档/暂存/数据目录重叠，不允许使用")
		}
	}
	var prefix []string
	if s.BasePath != "" {
		if prefix, err = checkRel(s.BasePath, false); err != nil {
			return nil, invalid("起始目录无效")
		}
	}
	root, err := os.OpenRoot(real)
	if err != nil {
		return nil, mapErr(err)
	}
	return &provider{root: root, prefix: prefix, readOnly: s.Bool("read_only", false)}, nil
}

func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, cloud.ErrNotFound), errors.Is(err, cloud.ErrExists), errors.Is(err, cloud.ErrPermission), errors.Is(err, cloud.ErrTooLarge):
		return err
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%w: %v", cloud.ErrNotFound, err)
	case errors.Is(err, fs.ErrExist):
		return fmt.Errorf("%w: %v", cloud.ErrExists, err)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%w: %v", cloud.ErrPermission, err)
	}
	if strings.Contains(err.Error(), "escapes from parent") {
		return fmt.Errorf("%w: path escapes the connection directory", cloud.ErrPermission)
	}
	return err
}

// full is the path below the connection directory for components (prefix + rel).
func (p *provider) full(parts []string) string {
	all := append(append([]string{}, p.prefix...), parts...)
	if len(all) == 0 {
		return "."
	}
	return strings.Join(all, "/")
}

// lstatChain Lstat-checks every component of prefix+parts: none may be a symlink; every non-final one must be a
// directory. It returns the FileInfo of the last component (nil, ErrNotFound when something is missing).
func (p *provider) lstatChain(parts []string) (fs.FileInfo, error) {
	all := append(append([]string{}, p.prefix...), parts...)
	if len(all) == 0 {
		return p.root.Stat(".")
	}
	var fi fs.FileInfo
	for i := range all {
		var err error
		fi, err = p.root.Lstat(strings.Join(all[:i+1], "/"))
		if err != nil {
			return nil, mapErr(err)
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: symbolic links are not followed", cloud.ErrPermission)
		}
		if i < len(all)-1 && !fi.IsDir() {
			return nil, fmt.Errorf("%w: not a directory", cloud.ErrNotFound)
		}
	}
	return fi, nil
}

func entryOf(rel string, fi fs.FileInfo) cloud.Entry {
	name := fi.Name()
	if rel == "" {
		name = ""
	}
	e := cloud.Entry{Name: name, Path: rel, IsDir: fi.IsDir(), ModTime: fi.ModTime()}
	if !fi.IsDir() {
		e.Size = fi.Size()
	}
	return e
}

func join(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

func (p *provider) List(ctx context.Context, dir string) ([]cloud.Entry, error) {
	parts, err := checkRel(dir, false)
	if err != nil {
		return nil, err
	}
	fi, err := p.lstatChain(parts)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%w: not a directory", cloud.ErrNotFound)
	}
	d, err := p.root.Open(p.full(parts))
	if err != nil {
		return nil, mapErr(err)
	}
	defer d.Close()
	var out []cloud.Entry
	for {
		des, err := d.ReadDir(256)
		for _, de := range des {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			n := de.Name()
			if strings.HasPrefix(n, ".") || de.Type()&fs.ModeSymlink != 0 || !(de.IsDir() || de.Type().IsRegular()) {
				continue
			}
			info, ierr := de.Info()
			if ierr != nil {
				continue
			}
			out = append(out, entryOf(join(dir, n), info))
			if len(out) >= maxEntries {
				cloud.SortEntries(out)
				return out, nil
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, mapErr(err)
		}
	}
	cloud.SortEntries(out)
	return out, nil
}

func (p *provider) Stat(_ context.Context, rel string) (cloud.Entry, error) {
	parts, err := checkRel(rel, true)
	if err != nil {
		return cloud.Entry{}, err
	}
	fi, err := p.lstatChain(parts)
	if err != nil {
		return cloud.Entry{}, err
	}
	if !fi.IsDir() && !fi.Mode().IsRegular() {
		return cloud.Entry{}, fmt.Errorf("%w: not a regular file", cloud.ErrNotFound)
	}
	return entryOf(rel, fi), nil
}

func (p *provider) Open(_ context.Context, rel string) (io.ReadCloser, int64, error) {
	parts, err := checkRel(rel, false)
	if err != nil {
		return nil, 0, err
	}
	fi, err := p.lstatChain(parts)
	if err != nil {
		return nil, 0, err
	}
	if !fi.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%w: not a regular file", cloud.ErrPermission)
	}
	f, err := p.root.OpenFile(p.full(parts), os.O_RDONLY|openExtra, 0)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	// Re-validate the opened descriptor (a swap after the Lstat may have put a FIFO/device/directory there).
	fi2, err := f.Stat()
	if err != nil || !fi2.Mode().IsRegular() {
		f.Close()
		return nil, 0, fmt.Errorf("%w: not a regular file", cloud.ErrPermission)
	}
	return f, fi2.Size(), nil
}

func (p *provider) mkdirAll(parts []string) error {
	all := append(append([]string{}, p.prefix...), parts...)
	for i := range all {
		sub := strings.Join(all[:i+1], "/")
		if err := p.root.Mkdir(sub, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
			return mapErr(err)
		}
		fi, err := p.root.Lstat(sub)
		if err != nil {
			return mapErr(err)
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: symbolic links are not followed", cloud.ErrPermission)
		}
		if !fi.IsDir() {
			return fmt.Errorf("%w: a file is in the way", cloud.ErrExists)
		}
	}
	return nil
}

func newTmpName() string {
	id := fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
	return tmpPrefix + id + randSuffix()
}

func (p *provider) PutNew(ctx context.Context, rel string, r io.Reader, size int64) (err error) {
	if p.readOnly {
		return fmt.Errorf("%w: connection is read-only", cloud.ErrPermission)
	}
	parts, err := checkRel(rel, true)
	if err != nil || len(parts) == 0 {
		if err == nil {
			err = &cloud.ValidationError{Code: "INVALID_PATH", Msg: "路径无效"}
		}
		return err
	}
	if size < 0 {
		return &cloud.ValidationError{Code: "INVALID_VALUE", Msg: "文件大小无效"}
	}
	dirParts, name := parts[:len(parts)-1], parts[len(parts)-1]
	if fi, lerr := p.lstatChain(parts); lerr == nil {
		_ = fi
		return cloud.ErrExists
	} else if !errors.Is(lerr, cloud.ErrNotFound) {
		return lerr
	}
	if err := p.mkdirAll(dirParts); err != nil {
		return err
	}
	dir := p.full(dirParts)
	final := join(dir, name)
	if dir == "." {
		final = name
	}
	tmp := join(dir, newTmpName())
	if dir == "." {
		tmp = newTmpName()
	}
	f, err := p.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return mapErr(err)
	}
	defer func() {
		if err != nil {
			f.Close()
			_ = p.root.Remove(tmp)
		}
	}()
	n, cerr := io.Copy(f, &ctxReader{ctx, r})
	if cerr != nil {
		return cerr
	}
	if n != size {
		return fmt.Errorf("size mismatch: wrote %d bytes, expected %d", n, size)
	}
	if err = f.Sync(); err != nil {
		return mapErr(err)
	}
	if err = f.Close(); err != nil {
		return mapErr(err)
	}
	// Re-check the directory chain right before publishing (symlink swap defence), then link without replacing.
	if _, err = p.lstatChain(dirParts); err != nil {
		return err
	}
	if err = p.root.Link(tmp, final); err != nil {
		if errors.Is(err, fs.ErrExist) {
			err = cloud.ErrExists
		} else {
			err = mapErr(err)
		}
		return err
	}
	_ = p.root.Remove(tmp)
	syncDir(p.root, dir)
	return nil
}

func syncDir(root *os.Root, dir string) {
	d, err := root.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync() // unsupported on some platforms/filesystems; best effort
	d.Close()
}

func (p *provider) Remove(_ context.Context, rel string) error {
	if p.readOnly {
		return fmt.Errorf("%w: connection is read-only", cloud.ErrPermission)
	}
	parts, err := checkRel(rel, true)
	if err != nil {
		return err
	}
	if len(parts) == 0 {
		return &cloud.ValidationError{Code: "INVALID_PATH", Msg: "路径无效"}
	}
	fi, err := p.lstatChain(parts)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: only regular files can be removed", cloud.ErrPermission)
	}
	return mapErr(p.root.Remove(p.full(parts)))
}

func (p *provider) Close() error { return p.root.Close() }

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(b)
}
