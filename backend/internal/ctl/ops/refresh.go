package ops

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"oa-agent/backend/internal/ctl/compose"
	"oa-agent/backend/internal/ctl/fsutil"
)

// DeployImageName is the repository (next to oa-agent-api / oa-agent-worker) that carries the installer bundle:
// compose files, oa-agentctl.sh, prebuilt oa-agentctl binaries and bin/SHA256SUMS under /deploy.
const DeployImageName = "oa-agent-deploy"

// ErrChecksum means the prebuilt oa-agentctl in the deploy image does not match bin/SHA256SUMS. It is never ignored.
var ErrChecksum = errors.New("oa-agentctl checksum mismatch")

// managedFiles are the files copied from the bundle into the install directory. .env, mounts.json and the generated
// docker-compose.mounts.yml are deliberately absent: they hold the installation's own state.
var managedFiles = []string{"docker-compose.yml", "docker-compose.build.yml", "docker-compose.https.yml", "Caddyfile.template", ".env.example"}

var defaultDirLine = regexp.MustCompile(`(?m)^OA_DEFAULT_DIR=.*$`)

// RegistryMode reports whether the stack runs pre-built registry images (no build layer in COMPOSE_FILE).
func (e Env) RegistryMode() bool {
	return !strings.Contains(e.P.Inst.ComposeFile(), "docker-compose.build.yml")
}

// DeployImage is the installer bundle image for tag, or "" when no registry is configured.
func (e Env) DeployImage(tag string) string {
	reg := strings.TrimRight(e.P.Inst.Env.Value("OA_IMAGE_REGISTRY", ""), "/")
	if reg == "" || tag == "" {
		return ""
	}
	return reg + "/" + DeployImageName + ":" + tag
}

// fetchBundle copies /deploy of image into dest: docker pull, docker create, docker cp, docker rm.
func (e Env) fetchBundle(ctx context.Context, pr Progress, image, dest string) error {
	if e.Fetch != nil {
		return e.Fetch(ctx, image, dest)
	}
	run := func(args ...string) (string, error) {
		res, err := e.P.R.Run(ctx, compose.Spec{Args: args})
		return res.Stdout, err
	}
	if err := e.P.R.Stream(ctx, compose.Spec{Args: []string{"pull", image}}, func(l string) { pr.say("  %s", l) }); err != nil {
		return err
	}
	out, err := run("create", image)
	if err != nil {
		return err
	}
	lines := strings.Fields(out)
	if len(lines) == 0 {
		return errors.New("docker create returned no container id")
	}
	cid := lines[len(lines)-1]
	defer func() { _, _ = run("rm", "-f", cid) }()
	_, err = run("cp", cid+":/deploy/.", dest)
	return err
}

// verifiedCtl returns the path of the prebuilt oa-agentctl for arch after checking it against bin/SHA256SUMS.
// "" with a nil error means the bundle has no binary for this architecture.
func verifiedCtl(dir, arch string) (string, error) {
	name := "oa-agentctl-linux-" + arch
	p := filepath.Join(dir, "bin", name)
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	sums, err := os.ReadFile(filepath.Join(dir, "bin", "SHA256SUMS"))
	if err != nil {
		return "", fmt.Errorf("%w: bin/SHA256SUMS is missing", ErrChecksum)
	}
	want := ""
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			want = strings.ToLower(f[0])
		}
	}
	got := sha256.Sum256(data)
	if want == "" || want != hex.EncodeToString(got[:]) {
		return "", fmt.Errorf("%w: %s", ErrChecksum, name)
	}
	return p, nil
}

// RefreshManagement replaces the installed management files (compose files, oa-agentctl.sh, and the Go binary after
// checksum verification) with the ones from the deploy image of tag. It never touches .env, mounts.json or the
// generated mounts override. A failure to fetch the bundle is returned as is; ErrChecksum means "do not continue".
func (e Env) RefreshManagement(ctx context.Context, tag string, pr Progress) error {
	image := e.DeployImage(tag)
	if image == "" {
		return errors.New("OA_IMAGE_REGISTRY is not set")
	}
	tmp, err := os.MkdirTemp("", "oa-deploy-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	pr.say("从镜像更新管理文件 %s …", image)
	if err := e.fetchBundle(ctx, pr, image, tmp); err != nil {
		return fmt.Errorf("无法获取 %s：%w", image, err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "docker-compose.yml")); err != nil {
		return fmt.Errorf("%s 不含 /deploy/docker-compose.yml", image)
	}
	arch := runtime.GOARCH
	bin, err := verifiedCtl(tmp, arch)
	if err != nil {
		return err
	}
	inst := e.P.Inst
	for _, f := range managedFiles {
		b, err := os.ReadFile(filepath.Join(tmp, f))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if err := fsutil.WriteAtomic(filepath.Join(inst.Dir, f), b, 0o644); err != nil {
			return err
		}
	}
	pr.say("已更新 compose 文件")
	if e.BinDir == "" {
		pr.say("未找到已安装的 oa-agentctl 路径，跳过脚本与二进制更新")
		return nil
	}
	if sh, err := os.ReadFile(filepath.Join(tmp, "oa-agentctl.sh")); err == nil {
		sh = defaultDirLine.ReplaceAll(sh, []byte("OA_DEFAULT_DIR='"+inst.Dir+"'"))
		if err := fsutil.WriteAtomic(filepath.Join(e.BinDir, "oa-agentctl.sh"), sh, 0o755); err != nil {
			return err
		}
	}
	if bin != "" {
		b, err := os.ReadFile(bin)
		if err != nil {
			return err
		}
		if err := fsutil.WriteAtomic(filepath.Join(e.BinDir, "oa-agentctl"), b, 0o755); err != nil {
			return err
		}
		pr.say("oa-agentctl 已更新（SHA-256 校验通过）")
	}
	return nil
}
