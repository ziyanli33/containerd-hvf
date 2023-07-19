package hvf

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/api/runtime/task/v2"
	task2 "github.com/containerd/containerd/api/types/task"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/log"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/pkg/process"
	"github.com/containerd/containerd/pkg/stdio"
	"github.com/containerd/containerd/runtime/v2/shim"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func Init(ctx context.Context, s string, publisher shim.Publisher, f func()) (shim.Shim, error) {

	svc := &TaskService{
		id:        s,
		context:   ctx,
		events:    make(chan interface{}, 128),
		sigs:      make(chan os.Signal, 1),
		cancel:    f,
		processes: make(map[string]process.Process),
		vm:        make(map[string]*VM),
	}

	go svc.forward(ctx, publisher)

	if address, err := shim.ReadAddress("address"); err == nil {
		svc.shimAddress = address
	}
	return svc, nil
}

// TaskService implements shim.Shim
type TaskService struct {
	mu sync.Mutex

	id        string
	events    chan interface{}
	context   context.Context
	sigs      chan os.Signal
	cancel    func()
	processes map[string]process.Process

	vm map[string]*VM

	shimAddress string
	f           *os.File
}

func (s *TaskService) State(ctx context.Context, r *task.StateRequest) (resp *task.StateResponse, err error) {
	defer logrus.WithError(err).WithFields(logrus.Fields{"req": r, "resp": resp}).Info("Task State")
	vm, ok := s.vm[r.ID]
	if !ok {
		return &task.StateResponse{}, errdefs.ToGRPC(errors.New("process not found"))
	}
	status, err := vm.Status(ctx)
	if err != nil {
		return &task.StateResponse{}, errdefs.ToGRPC(err)
	}

	return &task.StateResponse{
		ID:         vm.ID(),
		Bundle:     vm.bundle,
		Pid:        vm.Pid(),
		Status:     fromStatus(status),
		Stdin:      vm.stdio.Stdin,
		Stdout:     vm.stdio.Stdout,
		Stderr:     vm.stdio.Stderr,
		Terminal:   vm.stdio.Terminal,
		ExitStatus: uint32(vm.status),
		ExitedAt:   timestamppb.New(vm.exitedAt),
	}, nil
}

func fromStatus(s containerd.Status) task2.Status {
	switch s.Status {
	case containerd.Created:
		return task2.Status_CREATED
	case containerd.Running:
		return task2.Status_RUNNING
	case containerd.Stopped:
		return task2.Status_STOPPED
	case containerd.Paused:
		return task2.Status_PAUSED
	case containerd.Pausing:
		return task2.Status_PAUSING
	}
	return task2.Status_UNKNOWN
}

func (s *TaskService) Create(ctx context.Context, r *task.CreateTaskRequest) (resp *task.CreateTaskResponse, err error) {
	defer func() {
		logrus.WithError(err).WithFields(logrus.Fields{"req": r, "resp": resp}).Info("Task Create")
	}()
	logDir := filepath.Join("/var/log/containerd-shim-hvf-v1", r.ID)
	_ = os.MkdirAll(logDir, os.ModePerm)
	f, err := os.OpenFile(filepath.Join(logDir, "shim.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return &task.CreateTaskResponse{}, err
	}
	// Persist shim logs as rootfs must be empty when shim exits.
	s.f = f
	logrus.SetOutput(f)
	spec, err := readSpec()
	if err != nil {
		logrus.WithError(err).Error("read spec failed")
		return &task.CreateTaskResponse{}, errdefs.ToGRPC(err)
	}
	stdioObj := stdio.Stdio{
		Stdin:    r.Stdin,
		Stdout:   r.Stdout,
		Stderr:   r.Stderr,
		Terminal: r.Terminal,
	}

	vm, err := NewVM(r.ID, stdioObj, spec, r.Bundle, r.Rootfs)
	if err != nil {
		return &task.CreateTaskResponse{}, errdefs.ToGRPC(errors.Wrap(err, "failed to create VM"))
	}
	s.mu.Lock()
	s.vm[vm.ID()] = vm
	s.mu.Unlock()

	err = vm.Init()
	if err != nil {
		return &task.CreateTaskResponse{}, errdefs.ToGRPC(errors.Wrap(err, "failed to initialize VM"))
	}

	s.send(&events.TaskCreate{
		ContainerID: r.ID,
		Bundle:      r.Bundle,
		Rootfs:      r.Rootfs,
		IO: &events.TaskIO{
			Stdin:    r.Stdin,
			Stdout:   r.Stdout,
			Stderr:   r.Stderr,
			Terminal: r.Terminal,
		},
		Checkpoint: r.Checkpoint,
		Pid:        vm.Pid(),
	})

	return &task.CreateTaskResponse{
		Pid: vm.Pid(),
	}, nil
}

func (s *TaskService) Start(ctx context.Context, r *task.StartRequest) (resp *task.StartResponse, err error) {
	defer func() {
		logrus.WithError(err).WithFields(logrus.Fields{"req": r, "resp": resp}).Info("Task Start")
	}()
	vm, ok := s.vm[r.ID]
	if !ok {
		return nil, errdefs.ToGRPC(errors.New("process not found"))
	}

	err = vm.Start(ctx)
	if err != nil {
		logrus.WithError(err).Error("failed to start VM")
		return nil, errdefs.ToGRPC(err)
	}

	event := &events.TaskExecStarted{
		ContainerID: r.ID,
		ExecID:      r.ExecID,
		Pid:         vm.Pid(),
	}

	s.send(event)

	logrus.WithFields(logrus.Fields{"req": r, "resp": resp}).Info("Task Start")
	return &task.StartResponse{
		Pid: event.Pid,
	}, nil
}

func (s *TaskService) Delete(ctx context.Context, r *task.DeleteRequest) (resp *task.DeleteResponse, err error) {
	defer func() {
		logrus.WithError(err).WithField("req", r).WithField("resp", resp).Info("Task Delete")
	}()
	vm, ok := s.vm[r.ID]
	if !ok {
		return nil, errdefs.ToGRPC(errors.New("process not found"))
	}
	exitStatus, err := vm.Delete(ctx)
	if err != nil {
		return nil, errdefs.ToGRPC(errors.Wrap(err, "failed to delete process"))
	}
	if exitStatus.Error() != nil {
		logrus.WithError(exitStatus.Error()).Error("failed to release VM resources")
	}
	event := &events.TaskDelete{
		ContainerID: r.ID,
		Pid:         vm.Pid(),
		ExitStatus:  exitStatus.ExitCode(),
		ExitedAt:    timestamppb.New(exitStatus.ExitTime()),
	}

	// If we deleted an init task, send the task delete event.
	if r.ExecID == "" {
		s.send(event)
	}

	return &task.DeleteResponse{
		ExitStatus: event.ExitStatus,
		ExitedAt:   event.ExitedAt,
		Pid:        event.Pid,
	}, nil
}

func (s *TaskService) Pids(ctx context.Context, r *task.PidsRequest) (resp *task.PidsResponse, err error) {
	defer func() {
		logrus.WithFields(logrus.Fields{"req": r, "resp": resp}).Info("Task Pids")
	}()
	processes := make([]*task2.ProcessInfo, 0, len(s.processes))
	for _, p := range s.processes {
		processes = append(processes, &task2.ProcessInfo{
			Pid: uint32(p.Pid()),
		})
	}
	return &task.PidsResponse{
		Processes: processes,
	}, nil
}

func (s *TaskService) Pause(ctx context.Context, r *task.PauseRequest) (*emptypb.Empty, error) {
	defer logrus.WithFields(logrus.Fields{"req": r}).Info("Task Pause")
	return nil, nil
}

func (s *TaskService) Resume(ctx context.Context, r *task.ResumeRequest) (*emptypb.Empty, error) {
	defer logrus.WithFields(logrus.Fields{"req": r}).Info("Task Resume")
	return nil, nil
}

func (s *TaskService) Checkpoint(ctx context.Context, r *task.CheckpointTaskRequest) (*emptypb.Empty, error) {
	defer logrus.WithFields(logrus.Fields{"req": r}).Info("Task Checkpoint")
	return nil, nil
}

func (s *TaskService) Kill(ctx context.Context, r *task.KillRequest) (resp *emptypb.Empty, err error) {
	defer func() {
		logrus.WithError(err).WithFields(logrus.Fields{"req": r}).Info("Task Kill")
	}()
	vm, ok := s.vm[r.ID]
	if !ok {
		return nil, errdefs.ToGRPC(errors.New("process not found"))
	}
	err = vm.Kill(ctx, syscall.Signal(r.Signal))
	return &emptypb.Empty{}, err
}

func (s *TaskService) Exec(ctx context.Context, r *task.ExecProcessRequest) (*emptypb.Empty, error) {
	defer logrus.WithFields(logrus.Fields{"req": r}).Info("Task Exec")
	return nil, nil
}

func (s *TaskService) ResizePty(ctx context.Context, r *task.ResizePtyRequest) (*emptypb.Empty, error) {
	defer logrus.WithFields(logrus.Fields{"req": r}).Info("Task ResizePty")
	return nil, nil
}

func (s *TaskService) CloseIO(ctx context.Context, r *task.CloseIORequest) (*emptypb.Empty, error) {
	defer logrus.WithFields(logrus.Fields{"req": r}).Info("Task CloseIO")
	return nil, nil
}

func (s *TaskService) Update(ctx context.Context, r *task.UpdateTaskRequest) (*emptypb.Empty, error) {
	defer logrus.WithFields(logrus.Fields{"req": r}).Info("Task Update")
	return nil, nil
}

func (s *TaskService) Wait(ctx context.Context, r *task.WaitRequest) (resp *task.WaitResponse, err error) {
	defer func() {
		logrus.WithError(err).WithFields(logrus.Fields{"req": r, "resp": resp}).Info("Task Wait")
	}()
	vm, ok := s.vm[r.ID]
	if !ok {
		return nil, errdefs.ToGRPC(errors.New("process not found"))
	}
	waitChan, err := vm.Wait(ctx)
	if err != nil {
		return &task.WaitResponse{
			ExitStatus: 1,
			ExitedAt:   timestamppb.Now(),
		}, nil
	}
	exitStatus := <-waitChan
	return &task.WaitResponse{
		ExitStatus: exitStatus.ExitCode(),
		ExitedAt:   timestamppb.Now(),
	}, nil
}

func (s *TaskService) Stats(ctx context.Context, r *task.StatsRequest) (*task.StatsResponse, error) {
	defer logrus.WithFields(logrus.Fields{"req": r}).Info("Task Stats")
	return nil, nil
}

// Connect returns shim information such as the shim's pid
func (s *TaskService) Connect(ctx context.Context, r *task.ConnectRequest) (resp *task.ConnectResponse, err error) {
	defer func() {
		logrus.WithError(err).WithFields(logrus.Fields{"req": r, "resp": resp}).Info("Task Connect")
	}()
	var pid int
	if vm, ok := s.vm[r.ID]; ok {
		pid = int(vm.Pid())
	}

	return &task.ConnectResponse{
		ShimPid: uint32(os.Getpid()),
		TaskPid: uint32(pid),
	}, nil
}

func (s *TaskService) Shutdown(ctx context.Context, r *task.ShutdownRequest) (*emptypb.Empty, error) {
	defer logrus.WithFields(logrus.Fields{"req": r}).Info("Task Shutdown")
	if s.shimAddress != "" {
		_ = shim.RemoveSocket(s.shimAddress)
	}

	s.cancel()
	close(s.events)
	if s.f != nil {
		_ = s.f.Close()
	}
	return &emptypb.Empty{}, nil
}

func (s *TaskService) send(evt interface{}) {
	s.events <- evt
}

func (s *TaskService) forward(ctx context.Context, publisher shim.Publisher) {
	ns, _ := namespaces.Namespace(ctx)
	ctx = namespaces.WithNamespace(context.Background(), ns)
	for e := range s.events {
		topic := GetTopic(e)
		err := publisher.Publish(ctx, GetTopic(e), e)
		if err != nil {
			logrus.WithError(err).WithFields(log.Fields{"e": e, "topic": topic}).Error("post event fail")
		}
	}
	_ = publisher.Close()
}
