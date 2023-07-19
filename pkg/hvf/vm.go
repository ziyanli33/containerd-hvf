package hvf

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/api/types"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/pkg/stdio"
	"github.com/digitalocean/go-libvirt"
	"github.com/digitalocean/go-libvirt/socket/dialers"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"libvirt.org/go/libvirtxml"
)

var ErrInvalidImage = errors.New("invalid image")

const defaultRootImagePath = "disk"
const defaultRootImageFileName = "boot.qcow2"
const defaultCloudInitImageFileName = "cloudinit.iso"

type VM struct {
	id       string
	stdio    stdio.Stdio
	bundle   string
	pid      int
	status   int
	started  bool
	exited   bool
	exitedAt time.Time

	// spec is equivalent to config.json in the bundle
	spec   *specs.Spec
	mounts []*types.Mount
	env    map[string]string

	client     *libvirt.Libvirt
	domainMeta libvirt.Domain
	domain     *libvirtxml.Domain

	ctx    context.Context
	cancel context.CancelFunc
}

func NewVM(
	id string,
	stdio stdio.Stdio,
	spec *specs.Spec,
	bundle string,
	rootFS []*types.Mount,
) (*VM, error) {
	client := libvirt.NewWithDialer(dialers.NewLocal(dialers.WithSocket("/opt/homebrew/var/run/libvirt/libvirt-sock")))
	err := client.Connect()
	if err != nil {
		return nil, errors.Wrap(err, "failed to connect to libvirtd")
	}

	ctx, cancel := context.WithCancel(context.Background())
	env := make(map[string]string, 0)
	if spec.Process != nil {
		for _, s := range spec.Process.Env {
			split := strings.Split(s, "=")
			env[split[0]] = split[1]
		}
	}

	vm := &VM{
		id:     id,
		stdio:  stdio,
		spec:   spec,
		bundle: bundle,
		client: client,
		mounts: rootFS,
		env:    env,

		ctx:    ctx,
		cancel: cancel,
	}

	return vm, nil
}

// Init sets up root file system and define libvirt domain.
// As described at containerd github page,
// shims are responsible for mounting the filesystem into the rootfs/ directory of the bundle,
// also responsible for unmounting of the filesystem.
// During a delete binary call, the shim MUST ensure that filesystem is also unmounted(empty).
// Filesystems are provided by the containerd "native" snapshotters.
func (v *VM) Init() error {
	err := v.setupRootFS()
	if err != nil {
		return errors.Wrap(err, "failed to set up rootfs")
	}
	v.domain = RenderDomain(v.id, v.bundle)
	xmlString, err := v.domain.Marshal()
	if err != nil {
		return err
	}
	domainMeta, err := v.client.DomainDefineXML(xmlString)
	if err != nil {
		return err
	}
	v.domainMeta = domainMeta
	return nil
}

// QemuImageInfo represents the struct returned from `qemu-img info`.
type QemuImageInfo struct {
	Path        string `json:"-"`
	VirtualSize uint64 `json:"virtual-size"`
	Format      string `json:"format"`
	DirtyFlag   bool   `json:"dirty-flag"`
}

// setupRootFS simulate mount by using symlinks,
// as macOS doesn't have union-fs like feature, overlayfs or aufs snapshotters are not provided by containerd,
// Snapshotter can be listed by `ctr plugins ls`.
// See details https://github.com/containerd/containerd/blob/main/docs/snapshotters/README.md
//
//	Sample mount item: {
//		  type:"bind",
//		  source:"/var/lib/containerd/io.containerd.snapshotter.v1.native/snapshots/4",
//		  options:"rbind",
//		  options:"rw",
//	}
func (v *VM) setupRootFS() error {
	var bootImage string
	var cloudinitImage string
	var imagePath string
	for _, mount := range v.mounts {
		// We ignore non-bind mounts since those are not relevant to VM.
		if mount.Type != "bind" {
			continue
		}
		imagePath = filepath.Join(mount.Source, defaultRootImagePath)
		bootImage = filepath.Join(imagePath, defaultRootImageFileName)
		cloudinitImage = filepath.Join(imagePath, defaultCloudInitImageFileName)
	}
	if bootImage == "" {
		return errors.Wrap(ErrInvalidImage, "no bind type mounts")
	}
	_, err := os.Stat(bootImage)
	if err != nil {
		return err
	}
	bootInfo, err := getImageInfo(bootImage)
	if err != nil {
		return err
	}
	if bootInfo.Format != "qcow2" {
		return errors.Wrap(ErrInvalidImage, fmt.Sprintf("%v is not a qcow2 image", bootImage))
	}
	_, err = os.Stat(cloudinitImage)
	if err != nil {
		return err
	}
	return os.Symlink(imagePath, filepath.Join(v.bundle, "rootfs", defaultRootImagePath))
}

func getImageInfo(path string) (*QemuImageInfo, error) {
	cmd := exec.Command("qemu-img", "info", path, "--output=json")
	res, err := cmd.Output()
	if err != nil {
		return nil, errors.Wrap(err, fmt.Sprintf("failed to run qemu-img info at %v, err %v", path, cmd.Err.Error()))
	}
	imageInfo := &QemuImageInfo{}
	err = json.Unmarshal(res, imageInfo)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read qemu-img info")
	}

	if imageInfo.Format == "" {
		imageInfo.Format = "raw"
	}

	return imageInfo, nil
}

func (v *VM) ID() string {
	return v.id
}

func (v *VM) Pid() uint32 {
	if v.domain.Name == "" {
		return 0
	}
	if v.pid != 0 {
		return uint32(v.pid)
	}

	pidFile := fmt.Sprintf("/opt/homebrew/var/run/libvirt/qemu/%v.pid", v.domain.Name)
	stat, err := os.Stat(pidFile)
	if err != nil || stat.Size() == 0 {
		return 0
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		return 0
	}
	v.pid = pid
	return uint32(pid)
}

func (v *VM) Start(ctx context.Context) error {
	err := v.client.DomainCreate(v.domainMeta)
	if err != nil {
		return errors.Wrapf(err, "failed to start VM '%v'", v.domain.Name)
	}
	v.started = true
	return nil
}

func (v *VM) Delete(ctx context.Context, opts ...containerd.ProcessDeleteOpts) (*containerd.ExitStatus, error) {
	defer func() {
		removeErr := os.Remove(filepath.Join(v.bundle, "rootfs", defaultRootImagePath))
		if removeErr != nil {
			logrus.WithError(removeErr).Error("failed to remove image path")
		}
	}()
	err := v.client.DomainUndefineFlags(v.domainMeta, libvirt.DomainUndefineNvram)
	if err != nil && !libvirt.IsNotFound(err) {
		return containerd.NewExitStatus(1, v.exitedAt, err), nil
	}
	return containerd.NewExitStatus(0, v.exitedAt, nil), nil
}

func (v *VM) Kill(ctx context.Context, signal syscall.Signal, opts ...containerd.KillOpts) error {
	err := v.client.DomainDestroy(v.domainMeta)
	if err != nil {
		if libvirt.IsNotFound(err) || strings.Contains(err.Error(), "is not running") {
			// Already stopped.
			v.stdio.Terminal = true
			v.exitedAt = time.Now()
			v.exited = true
			v.cancel()
			return nil
		}
		logrus.WithError(err).Error("failed to destroy domain")
		return errors.Wrapf(err, "failed to stop VM '%v'", v.domain.Name)
	}
	v.stdio.Terminal = true
	v.exitedAt = time.Now()
	v.exited = true
	v.cancel()
	return nil
}

func (v *VM) Wait(ctx context.Context) (<-chan containerd.ExitStatus, error) {
	ctx, cancel := context.WithCancel(ctx)
	ticker := time.NewTicker(time.Second)
	exitChan := make(chan containerd.ExitStatus, 1)
	go func() {
		for {
			select {
			case <-ticker.C:
				status, err := v.Status(ctx)
				if err == nil && status.Status == containerd.Stopped {
					ticker.Stop()
					cancel()
					return
				}
			case <-v.ctx.Done():
				// Kill was invoked.
				status, err := v.Status(ctx)
				if err == nil && status.Status == containerd.Stopped {
					cancel()
					return
				}
				// It takes a few seconds before v.Status becomes "stopped". Avoid spin-lock.
				time.Sleep(time.Second)
			case <-ctx.Done():
				return
			}
		}
	}()
	<-ctx.Done()
	// Always exit with 0.
	exitChan <- *containerd.NewExitStatus(0, time.Now(), nil)
	return exitChan, nil
}

func (v *VM) CloseIO(ctx context.Context, opts ...containerd.IOCloserOpts) error {
	//TODO implement me
	panic("implement me")
}

func (v *VM) Resize(ctx context.Context, w, h uint32) error {
	//TODO implement me
	panic("implement me")
}

func (v *VM) IO() cio.IO {
	//TODO implement me
	panic("implement me")
}

func (v *VM) Status(ctx context.Context) (containerd.Status, error) {
	if !v.started {
		return containerd.Status{
			Status: containerd.Created,
		}, nil
	}

	pid := v.Pid()
	if pid == 0 {
		if v.exited {
			return containerd.Status{
				Status: containerd.Stopped,
			}, nil
		}
		return containerd.Status{
			Status: containerd.Unknown,
		}, nil
	}

	// See https://github.com/libvirt/libvirt/blob/v9.5.0/include/libvirt/libvirt-domain.h#L57.
	// VIR_DOMAIN_NOSTATE = 0,     /* no state (Since: 0.0.1) */
	// VIR_DOMAIN_RUNNING = 1,     /* the domain is running (Since: 0.0.1) */
	// VIR_DOMAIN_BLOCKED = 2,     /* the domain is blocked on resource (Since: 0.0.1) */
	// VIR_DOMAIN_PAUSED  = 3,     /* the domain is paused by user (Since: 0.0.1) */
	// VIR_DOMAIN_SHUTDOWN= 4,     /* the domain is being shut down (Since: 0.0.1) */
	// VIR_DOMAIN_SHUTOFF = 5,     /* the domain is shut off (Since: 0.0.1) */
	// VIR_DOMAIN_CRASHED = 6,     /* the domain is crashed (Since: 0.0.2) */
	// VIR_DOMAIN_PMSUSPENDED = 7, /* the domain is suspended by guest power management (Since: 0.9.11) */
	virDomainState, _, err := v.client.DomainGetState(v.domainMeta, 0)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			// Domain is probably removed.
			if v.exited {
				return containerd.Status{
					Status: containerd.Stopped,
				}, nil
			}
			return containerd.Status{
				Status: containerd.Unknown,
			}, nil
		}
		return containerd.Status{
			Status: containerd.Unknown,
		}, err
	}
	switch virDomainState {
	case 0: // Defined but no state.
		return containerd.Status{
			Status: containerd.Created,
		}, nil
	case 1: // Running.
		return containerd.Status{
			Status: containerd.Running,
		}, nil
	case 3, 7: // Paused, suspended.
		return containerd.Status{
			Status: containerd.Paused,
		}, nil
	case 4: // Shutting down.
		return containerd.Status{
			Status: containerd.Pausing,
		}, nil
	case 5, 6: // Shut off, crashed.
		// Special handling for preserving shim if needed.
		return containerd.Status{
			Status: containerd.Stopped,
		}, nil
	default:
		return containerd.Status{
			Status: containerd.Unknown,
		}, nil
	}
}
