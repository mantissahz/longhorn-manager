package client

import (
	"context"
	"crypto/tls"
	"fmt"

	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	rpc "github.com/longhorn/types/pkg/generated/imrpc"

	"github.com/longhorn/longhorn-instance-manager/pkg/api"
	"github.com/longhorn/longhorn-instance-manager/pkg/meta"
	"github.com/longhorn/longhorn-instance-manager/pkg/types"
	"github.com/longhorn/longhorn-instance-manager/pkg/util"
)

type InstanceServiceContext struct {
	cc *grpc.ClientConn

	ctx  context.Context
	quit context.CancelFunc

	service rpc.InstanceServiceClient
	health  healthpb.HealthClient
}

func (c InstanceServiceContext) Close() error {
	c.quit()
	if c.cc == nil {
		return nil
	}
	if err := c.cc.Close(); err != nil {
		return errors.Wrap(err, "failed to close instance gRPC connection")
	}
	return nil
}

func (c *InstanceServiceClient) getControllerServiceClient() rpc.InstanceServiceClient {
	return c.service
}

type InstanceServiceClient struct {
	serviceURL string
	tlsConfig  *tls.Config
	InstanceServiceContext
}

func NewInstanceServiceClient(ctx context.Context, ctxCancel context.CancelFunc, serviceURL string, tlsConfig *tls.Config) (*InstanceServiceClient, error) {
	getInstanceServiceContext := func(serviceUrl string, tlsConfig *tls.Config) (InstanceServiceContext, error) {
		connection, err := util.Connect(serviceUrl, tlsConfig)
		if err != nil {
			return InstanceServiceContext{}, errors.Wrapf(err, "cannot connect to Instance Service %v", serviceUrl)
		}

		return InstanceServiceContext{
			cc:      connection,
			ctx:     ctx,
			quit:    ctxCancel,
			service: rpc.NewInstanceServiceClient(connection),
			health:  healthpb.NewHealthClient(connection),
		}, nil
	}

	serviceContext, err := getInstanceServiceContext(serviceURL, tlsConfig)
	if err != nil {
		return nil, err
	}

	return &InstanceServiceClient{
		serviceURL:             serviceURL,
		tlsConfig:              tlsConfig,
		InstanceServiceContext: serviceContext,
	}, nil
}

func NewInstanceServiceClientWithTLS(ctx context.Context, ctxCancel context.CancelFunc, serviceURL, caFile, certFile, keyFile, peerName string) (*InstanceServiceClient, error) {
	tlsConfig, err := util.LoadClientTLS(caFile, certFile, keyFile, peerName)
	if err != nil {
		return nil, errors.Wrap(err, "failed to load tls key pair from file")
	}

	return NewInstanceServiceClient(ctx, ctxCancel, serviceURL, tlsConfig)
}

type EngineCreateRequest struct {
	ReplicaAddressMap map[string]string
	Frontend          string
	InitiatorAddress  string
	TargetAddress     string
	UpgradeRequired   bool
	SalvageRequested  bool
}

type ReplicaCreateRequest struct {
	DiskName         string
	DiskUUID         string
	ExposeRequired   bool
	BackingImageName string
}

type InstanceCreateRequest struct {
	DataEngine   string
	Name         string
	InstanceType string
	VolumeName   string
	Size         uint64
	PortCount    int
	PortArgs     []string

	Binary     string
	BinaryArgs []string

	Engine  EngineCreateRequest
	Replica ReplicaCreateRequest

	// Deprecated: replaced by DataEngine.
	BackendStoreDriver string
}

// InstanceCreate creates an instance.
func (c *InstanceServiceClient) InstanceCreate(req *InstanceCreateRequest) (*api.Instance, error) {
	if req.Name == "" || req.InstanceType == "" {
		return nil, fmt.Errorf("failed to create instance: missing required parameter")
	}

	driver, ok := rpc.DataEngine_value[getDataEngine(req.DataEngine)]
	if !ok {
		return nil, fmt.Errorf("failed to delete instance: invalid data engine %v", req.DataEngine)
	}

	client := c.getControllerServiceClient()
	ctx, cancel := context.WithTimeout(context.Background(), types.GRPCServiceTimeout)
	defer cancel()

	var processInstanceSpec *rpc.ProcessInstanceSpec
	var spdkInstanceSpec *rpc.SpdkInstanceSpec
	if rpc.DataEngine(driver) == rpc.DataEngine_DATA_ENGINE_V1 {
		processInstanceSpec = &rpc.ProcessInstanceSpec{
			Binary: req.Binary,
			Args:   req.BinaryArgs,
		}
	} else {
		switch req.InstanceType {
		case types.InstanceTypeEngine:
			spdkInstanceSpec = &rpc.SpdkInstanceSpec{
				Size:              req.Size,
				ReplicaAddressMap: req.Engine.ReplicaAddressMap,
				Frontend:          req.Engine.Frontend,
				SalvageRequested:  req.Engine.SalvageRequested,
			}
		case types.InstanceTypeReplica:
			spdkInstanceSpec = &rpc.SpdkInstanceSpec{
				Size:             req.Size,
				DiskName:         req.Replica.DiskName,
				DiskUuid:         req.Replica.DiskUUID,
				ExposeRequired:   req.Replica.ExposeRequired,
				BackingImageName: req.Replica.BackingImageName,
			}
		default:
			return nil, fmt.Errorf("failed to create instance: invalid instance type %v", req.InstanceType)
		}
	}

	p, err := client.InstanceCreate(ctx, &rpc.InstanceCreateRequest{
		Spec: &rpc.InstanceSpec{
			// nolint:all replaced with DataEngine
			BackendStoreDriver: rpc.BackendStoreDriver(driver),
			DataEngine:         rpc.DataEngine(driver),
			Name:               req.Name,
			Type:               req.InstanceType,
			VolumeName:         req.VolumeName,
			PortCount:          int32(req.PortCount),
			PortArgs:           req.PortArgs,

			ProcessInstanceSpec: processInstanceSpec,
			SpdkInstanceSpec:    spdkInstanceSpec,

			UpgradeRequired:  req.Engine.UpgradeRequired,
			InitiatorAddress: req.Engine.InitiatorAddress,
			TargetAddress:    req.Engine.TargetAddress,
		},
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to create instance")
	}

	return api.RPCToInstance(p), nil
}

// InstanceDelete deletes the instance by name. UUID will be validated if not empty.
func (c *InstanceServiceClient) InstanceDelete(dataEngine, name, uuid, instanceType, diskUUID string, cleanupRequired bool) (*api.Instance, error) {
	if name == "" {
		return nil, fmt.Errorf("failed to delete instance: missing required parameter name")
	}

	driver, ok := rpc.DataEngine_value[getDataEngine(dataEngine)]
	if !ok {
		return nil, fmt.Errorf("failed to delete instance: invalid data engine %v", dataEngine)
	}

	client := c.getControllerServiceClient()
	ctx, cancel := context.WithTimeout(context.Background(), types.GRPCServiceTimeout)
	defer cancel()

	p, err := client.InstanceDelete(ctx, &rpc.InstanceDeleteRequest{
		Name: name,
		Uuid: uuid,
		Type: instanceType,
		// nolint:all replaced with DataEngine
		BackendStoreDriver: rpc.BackendStoreDriver(driver),
		DataEngine:         rpc.DataEngine(driver),
		DiskUuid:           diskUUID,
		CleanupRequired:    cleanupRequired,
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to delete instance %v", name)
	}
	return api.RPCToInstance(p), nil
}

// InstanceGet returns the instance by name.
func (c *InstanceServiceClient) InstanceGet(dataEngine, name, instanceType string) (*api.Instance, error) {
	if name == "" {
		return nil, fmt.Errorf("failed to get instance: missing required parameter name")
	}

	driver, ok := rpc.DataEngine_value[getDataEngine(dataEngine)]
	if !ok {
		return nil, fmt.Errorf("failed to get instance: invalid data engine %v", dataEngine)
	}

	client := c.getControllerServiceClient()
	ctx, cancel := context.WithTimeout(context.Background(), types.GRPCServiceTimeout)
	defer cancel()

	p, err := client.InstanceGet(ctx, &rpc.InstanceGetRequest{
		Name: name,
		Type: instanceType,
		// nolint:all replaced with DataEngine
		BackendStoreDriver: rpc.BackendStoreDriver(driver),
		DataEngine:         rpc.DataEngine(driver),
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get instance %v", name)
	}
	return api.RPCToInstance(p), nil
}

func (c *InstanceServiceClient) InstanceList() (map[string]*api.Instance, error) {
	client := c.getControllerServiceClient()
	ctx, cancel := context.WithTimeout(context.Background(), types.GRPCServiceTimeout)
	defer cancel()

	instances, err := client.InstanceList(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list instances")
	}
	return api.RPCToInstanceList(instances), nil
}

// InstanceLog returns the log stream of an instance.
func (c *InstanceServiceClient) InstanceLog(ctx context.Context, dataEngine, name, instanceType string) (*api.LogStream, error) {
	if name == "" {
		return nil, fmt.Errorf("failed to get instance: missing required parameter name")
	}

	driver, ok := rpc.DataEngine_value[getDataEngine(dataEngine)]
	if !ok {
		return nil, fmt.Errorf("failed to log instance: invalid data engine %v", dataEngine)
	}

	client := c.getControllerServiceClient()
	stream, err := client.InstanceLog(ctx, &rpc.InstanceLogRequest{
		Name: name,
		Type: instanceType,
		// nolint:all replaced with DataEngine
		BackendStoreDriver: rpc.BackendStoreDriver(driver),
		DataEngine:         rpc.DataEngine(driver),
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get instance log of %v", name)
	}
	return api.NewLogStream(stream), nil
}

// InstanceWatch watches for instance updates.
func (c *InstanceServiceClient) InstanceWatch(ctx context.Context) (*api.InstanceStream, error) {
	client := c.getControllerServiceClient()
	stream, err := client.InstanceWatch(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, errors.Wrap(err, "failed to open instance update stream")
	}

	return api.NewInstanceStream(stream), nil
}

// InstanceReplace replaces an instance with a new one.
func (c *InstanceServiceClient) InstanceReplace(dataEngine, name, instanceType, binary string, portCount int, args, portArgs []string, terminateSignal string) (*api.Instance, error) {
	if name == "" || binary == "" {
		return nil, fmt.Errorf("failed to replace instance: missing required parameter")
	}

	driver, ok := rpc.DataEngine_value[getDataEngine(dataEngine)]
	if !ok {
		return nil, fmt.Errorf("failed to replace instance: invalid data engine %v", dataEngine)
	}

	if terminateSignal != "SIGHUP" {
		return nil, fmt.Errorf("unsupported terminate signal %v", terminateSignal)
	}

	client := c.getControllerServiceClient()
	ctx, cancel := context.WithTimeout(context.Background(), types.GRPCServiceTimeout)
	defer cancel()

	p, err := client.InstanceReplace(ctx, &rpc.InstanceReplaceRequest{
		Spec: &rpc.InstanceSpec{
			Name: name,
			Type: instanceType,
			// nolint:all replaced with DataEngine
			BackendStoreDriver: rpc.BackendStoreDriver(driver),
			DataEngine:         rpc.DataEngine(driver),
			ProcessInstanceSpec: &rpc.ProcessInstanceSpec{
				Binary: binary,
				Args:   args,
			},
			PortCount: int32(portCount),
			PortArgs:  portArgs,
		},
		TerminateSignal: terminateSignal,
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to replace instance")
	}
	return api.RPCToInstance(p), nil
}

// InstanceSuspend suspends an instance.
func (c *InstanceServiceClient) InstanceSuspend(dataEngine, name, instanceType string) error {
	if name == "" {
		return fmt.Errorf("failed to suspend instance: missing required parameter name")
	}

	driver, ok := rpc.DataEngine_value[getDataEngine(dataEngine)]
	if !ok {
		return fmt.Errorf("failed to suspend instance: invalid data engine %v", dataEngine)
	}

	client := c.getControllerServiceClient()
	ctx, cancel := context.WithTimeout(context.Background(), types.GRPCServiceTimeout)
	defer cancel()

	_, err := client.InstanceSuspend(ctx, &rpc.InstanceSuspendRequest{
		Name:       name,
		Type:       instanceType,
		DataEngine: rpc.DataEngine(driver),
	})
	if err != nil {
		return errors.Wrapf(err, "failed to suspend instance %v", name)
	}

	return nil
}

// InstanceResume suspends an instance.
func (c *InstanceServiceClient) InstanceResume(dataEngine, name, instanceType string) error {
	if name == "" {
		return fmt.Errorf("failed to resume instance: missing required parameter name")
	}

	driver, ok := rpc.DataEngine_value[getDataEngine(dataEngine)]
	if !ok {
		return fmt.Errorf("failed to resume instance: invalid data engine %v", dataEngine)
	}

	client := c.getControllerServiceClient()
	ctx, cancel := context.WithTimeout(context.Background(), types.GRPCServiceTimeout)
	defer cancel()

	_, err := client.InstanceResume(ctx, &rpc.InstanceResumeRequest{
		Name:       name,
		Type:       instanceType,
		DataEngine: rpc.DataEngine(driver),
	})
	if err != nil {
		return errors.Wrapf(err, "failed to resume instance %v", name)
	}

	return nil
}

// InstanceSwitchOverTarget switches over the target for an instance.
func (c *InstanceServiceClient) InstanceSwitchOverTarget(dataEngine, name, instanceType, targetAddress string) error {
	if name == "" {
		return fmt.Errorf("failed to switch over target for instance: missing required parameter name")
	}

	if targetAddress == "" {
		return fmt.Errorf("failed to switch over target for instance: missing required parameter target address")
	}

	driver, ok := rpc.DataEngine_value[getDataEngine(dataEngine)]
	if !ok {
		return fmt.Errorf("failed to switch over target instance: invalid data engine %v", dataEngine)
	}

	client := c.getControllerServiceClient()
	ctx, cancel := context.WithTimeout(context.Background(), types.GRPCServiceTimeout)
	defer cancel()

	_, err := client.InstanceSwitchOverTarget(ctx, &rpc.InstanceSwitchOverTargetRequest{
		Name:          name,
		Type:          instanceType,
		DataEngine:    rpc.DataEngine(driver),
		TargetAddress: targetAddress,
	})
	if err != nil {
		return errors.Wrapf(err, "failed to switch over target for instance %v", name)
	}

	return nil
}

// InstanceDeleteTarget delete target for an instance.
func (c *InstanceServiceClient) InstanceDeleteTarget(dataEngine, name, instanceType string) error {
	if name == "" {
		return fmt.Errorf("failed to delete target for instance: missing required parameter name")
	}

	driver, ok := rpc.DataEngine_value[getDataEngine(dataEngine)]
	if !ok {
		return fmt.Errorf("failed to delete target instance: invalid data engine %v", dataEngine)
	}

	client := c.getControllerServiceClient()
	ctx, cancel := context.WithTimeout(context.Background(), types.GRPCServiceTimeout)
	defer cancel()

	_, err := client.InstanceDeleteTarget(ctx, &rpc.InstanceDeleteTargetRequest{
		Name:       name,
		Type:       instanceType,
		DataEngine: rpc.DataEngine(driver),
	})
	if err != nil {
		return errors.Wrapf(err, "failed to delete target for instance %v", name)
	}

	return nil
}

// InstanceResume resumes an instance.
func (c *InstanceServiceClient) VersionGet() (*meta.VersionOutput, error) {
	client := c.getControllerServiceClient()
	ctx, cancel := context.WithTimeout(context.Background(), types.GRPCServiceTimeout)
	defer cancel()

	resp, err := client.VersionGet(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, errors.Wrap(err, "failed to get version")
	}

	return &meta.VersionOutput{
		Version:   resp.Version,
		GitCommit: resp.GitCommit,
		BuildDate: resp.BuildDate,

		InstanceManagerAPIVersion:    int(resp.InstanceManagerAPIVersion),
		InstanceManagerAPIMinVersion: int(resp.InstanceManagerAPIMinVersion),

		InstanceManagerProxyAPIVersion:    int(resp.InstanceManagerProxyAPIVersion),
		InstanceManagerProxyAPIMinVersion: int(resp.InstanceManagerProxyAPIMinVersion),
	}, nil
}

// LogSetLevel sets the log level.
func (c *InstanceServiceClient) LogSetLevel(dataEngine, service, level string) error {
	client := c.getControllerServiceClient()
	ctx, cancel := context.WithTimeout(context.Background(), types.GRPCServiceTimeout)
	defer cancel()

	driver, ok := rpc.DataEngine_value[getDataEngine(dataEngine)]
	if !ok {
		return fmt.Errorf("failed to set log level: invalid data engine %v", dataEngine)
	}

	_, err := client.LogSetLevel(ctx, &rpc.LogSetLevelRequest{
		DataEngine: rpc.DataEngine(driver),
		Level:      level,
	})
	return err
}

// LogSetFlags sets the log flags.
func (c *InstanceServiceClient) LogSetFlags(dataEngine, service, flags string) error {
	client := c.getControllerServiceClient()
	ctx, cancel := context.WithTimeout(context.Background(), types.GRPCServiceTimeout)
	defer cancel()

	driver, ok := rpc.DataEngine_value[getDataEngine(dataEngine)]
	if !ok {
		return fmt.Errorf("failed to set log flags: invalid data engine %v", dataEngine)
	}

	_, err := client.LogSetFlags(ctx, &rpc.LogSetFlagsRequest{
		DataEngine: rpc.DataEngine(driver),
		Flags:      flags,
	})
	return err
}

// LogGetLevel returns the log level.
func (c *InstanceServiceClient) LogGetLevel(dataEngine, service string) (string, error) {
	client := c.getControllerServiceClient()
	ctx, cancel := context.WithTimeout(context.Background(), types.GRPCServiceTimeout)
	defer cancel()

	driver, ok := rpc.DataEngine_value[getDataEngine(dataEngine)]
	if !ok {
		return "", fmt.Errorf("failed to get log level: invalid data engine %v", dataEngine)
	}

	resp, err := client.LogGetLevel(ctx, &rpc.LogGetLevelRequest{
		DataEngine: rpc.DataEngine(driver),
	})
	if err != nil {
		return "", err
	}
	return resp.Level, nil
}

// LogGetFlags returns the log flags.
func (c *InstanceServiceClient) LogGetFlags(dataEngine, service string) (string, error) {
	client := c.getControllerServiceClient()
	ctx, cancel := context.WithTimeout(context.Background(), types.GRPCServiceTimeout)
	defer cancel()

	driver, ok := rpc.DataEngine_value[getDataEngine(dataEngine)]
	if !ok {
		return "", fmt.Errorf("failed to get log flags: invalid data engine %v", dataEngine)
	}

	resp, err := client.LogGetFlags(ctx, &rpc.LogGetFlagsRequest{
		DataEngine: rpc.DataEngine(driver),
	})
	if err != nil {
		return "", err
	}
	return resp.Flags, nil
}

func (c *InstanceServiceClient) CheckConnection() error {
	req := &healthpb.HealthCheckRequest{}
	_, err := c.health.Check(getContextWithGRPCTimeout(c.ctx), req)
	return err
}
