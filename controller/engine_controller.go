package controller

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	"golang.org/x/time/rate"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/flowcontrol"
	"k8s.io/kubernetes/pkg/controller"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientset "k8s.io/client-go/kubernetes"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"

	lhexec "github.com/longhorn/go-common-libs/exec"
	lhtypes "github.com/longhorn/go-common-libs/types"

	etypes "github.com/longhorn/longhorn-engine/pkg/types"
	imapi "github.com/longhorn/longhorn-instance-manager/pkg/api"
	imclient "github.com/longhorn/longhorn-instance-manager/pkg/client"
	imutil "github.com/longhorn/longhorn-instance-manager/pkg/util"

	"github.com/longhorn/longhorn-manager/constant"
	"github.com/longhorn/longhorn-manager/datastore"
	"github.com/longhorn/longhorn-manager/engineapi"
	"github.com/longhorn/longhorn-manager/types"
	"github.com/longhorn/longhorn-manager/util"

	longhorn "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta2"
)

const (
	unknownReplicaPrefix            = "UNKNOWN-"
	restoreGetLockFailedPatternMsg  = "error initiating (full|incremental) backup restore: failed lock"
	restoreAlreadyInProgressMsg     = "already in progress"
	restoreAlreadyRestoredBackupMsg = "already restored backup"
)

var (
	EnginePollInterval = 5 * time.Second
	EnginePollTimeout  = 30 * time.Second

	EngineMonitorConflictRetryCount = 5

	purgeWaitIntervalInSecond = 24 * 60 * 60

	// restoreMaxInterval: deleting the backup of big size volume takes a long time for retain policy and restoring backups would be in backoff period.
	restoreMaxInterval = 1 * time.Hour

	// amount of time between actual size updates that do not exceed threshold during periods with stable writes
	sizeUpdateLimit = 30 * time.Second
	// number of consecutive actual size updates allowed during bursts
	sizeUpdateBurst = 3
)

const (
	ConflictRetryCount = 5
)

type EngineController struct {
	*baseController

	// which namespace controller is running with
	namespace string
	// use as the OwnerID of the controller
	controllerID string

	kubeClient    clientset.Interface
	eventRecorder record.EventRecorder

	ds *datastore.DataStore

	cacheSyncs []cache.InformerSynced

	backoff *flowcontrol.Backoff

	instanceHandler *InstanceHandler

	engines            engineapi.EngineClientCollection
	engineMonitorMutex *sync.RWMutex
	engineMonitorMap   map[string]chan struct{}

	proxyConnCounter util.Counter

	restoringCounter      util.Counter
	restoringCounterMutex *sync.Mutex
}

type EngineMonitor struct {
	logger logrus.FieldLogger

	namespace     string
	ds            *datastore.DataStore
	eventRecorder record.EventRecorder

	Name             string
	engines          engineapi.EngineClientCollection
	stopCh           chan struct{}
	expansionBackoff *flowcontrol.Backoff
	restoreBackoff   *flowcontrol.Backoff

	expansionUpdateTime time.Time

	controllerID string
	// used to notify the controller that monitoring has stopped
	monitorVoluntaryStopCh chan struct{}

	proxyConnCounter util.Counter

	restoringCounter         util.Counter
	restoringCounterAcquired bool
	restoringCounterMutex    *sync.Mutex

	sizeUpdateLimiter *rate.Limiter
}

func NewEngineController(
	logger logrus.FieldLogger,
	ds *datastore.DataStore,
	scheme *runtime.Scheme,
	kubeClient clientset.Interface,
	engines engineapi.EngineClientCollection,
	namespace string, controllerID string,
	proxyConnCounter util.Counter) (*EngineController, error) {

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(logrus.Infof)
	// TODO: remove the wrapper when every clients have moved to use the clientset.
	eventBroadcaster.StartRecordingToSink(&v1core.EventSinkImpl{Interface: v1core.New(kubeClient.CoreV1().RESTClient()).Events("")})

	ec := &EngineController{
		baseController: newBaseController("longhorn-engine", logger),

		ds:        ds,
		namespace: namespace,

		controllerID:  controllerID,
		kubeClient:    kubeClient,
		eventRecorder: eventBroadcaster.NewRecorder(scheme, corev1.EventSource{Component: "longhorn-engine-controller"}),

		backoff: flowcontrol.NewBackOff(time.Second*10, time.Minute*5),

		engines:            engines,
		engineMonitorMutex: &sync.RWMutex{},
		engineMonitorMap:   map[string]chan struct{}{},

		proxyConnCounter:      proxyConnCounter,
		restoringCounter:      util.NewAtomicCounter(),
		restoringCounterMutex: &sync.Mutex{},
	}
	ec.instanceHandler = NewInstanceHandler(ds, ec, ec.eventRecorder)

	var err error
	if _, err = ds.EngineInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ec.enqueueEngine,
		UpdateFunc: func(old, cur interface{}) { ec.enqueueEngine(cur) },
		DeleteFunc: ec.enqueueEngine,
	}); err != nil {
		return nil, err
	}
	ec.cacheSyncs = append(ec.cacheSyncs, ds.EngineInformer.HasSynced)

	if _, err = ds.InstanceManagerInformer.AddEventHandlerWithResyncPeriod(cache.ResourceEventHandlerFuncs{
		AddFunc:    ec.enqueueInstanceManagerChange,
		UpdateFunc: func(old, cur interface{}) { ec.enqueueInstanceManagerChange(cur) },
		DeleteFunc: ec.enqueueInstanceManagerChange,
	}, 0); err != nil {
		return nil, err
	}
	ec.cacheSyncs = append(ec.cacheSyncs, ds.InstanceManagerInformer.HasSynced)

	return ec, nil
}

func (ec *EngineController) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ec.queue.ShutDown()

	ec.logger.Info("Starting Longhorn engine controller")
	defer ec.logger.Info("Shut down Longhorn engine controller")

	if !cache.WaitForNamedCacheSync("longhorn engines", stopCh, ec.cacheSyncs...) {
		return
	}

	for i := 0; i < workers; i++ {
		go wait.Until(ec.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (ec *EngineController) worker() {
	for ec.processNextWorkItem() {
	}
}

func (ec *EngineController) processNextWorkItem() bool {
	key, quit := ec.queue.Get()

	if quit {
		return false
	}
	defer ec.queue.Done(key)

	err := ec.syncEngine(key.(string))
	ec.handleErr(err, key)

	return true
}

func (ec *EngineController) handleErr(err error, key interface{}) {
	if err == nil {
		ec.queue.Forget(key)
		return
	}

	log := ec.logger.WithField("engine", key)
	if ec.queue.NumRequeues(key) < maxRetries {
		handleReconcileErrorLogging(log, err, "Failed to sync Longhorn engine")
		ec.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	handleReconcileErrorLogging(log, err, "Dropping Longhorn engine out of the queue")
	ec.queue.Forget(key)
}

func getLoggerForEngine(logger logrus.FieldLogger, e *longhorn.Engine) *logrus.Entry {
	return logger.WithField("engine", e.Name)
}

func (ec *EngineController) getEngineClientProxy(e *longhorn.Engine, image string) (engineapi.EngineClientProxy, error) {
	engineCliClient, err := GetBinaryClientForEngine(e, ec.engines, image)
	if err != nil {
		return nil, err
	}

	return engineapi.GetCompatibleClient(e, engineCliClient, ec.ds, ec.logger, ec.proxyConnCounter)
}

func (ec *EngineController) syncEngine(key string) (err error) {
	defer func() {
		err = errors.Wrapf(err, "failed to sync engine for %v", key)
	}()
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	if namespace != ec.namespace {
		// Not ours, don't do anything
		return nil
	}

	log := ec.logger.WithField("engine", name)
	engine, err := ec.ds.GetEngine(name)
	if err != nil {
		if datastore.ErrorIsNotFound(err) {
			return nil
		}
		return errors.Wrapf(err, "failed to get engine")
	}

	defaultEngineImage, err := ec.ds.GetSettingValueExisted(types.SettingNameDefaultEngineImage)
	if err != nil {
		return err
	}

	isResponsible, err := ec.isResponsibleFor(engine, defaultEngineImage)
	if err != nil {
		return err
	}
	if !isResponsible {
		return nil
	}

	if engine.Status.OwnerID != ec.controllerID {
		engine.Status.OwnerID = ec.controllerID
		engine, err = ec.ds.UpdateEngineStatus(engine)
		if err != nil {
			// we don't mind others coming first
			if apierrors.IsConflict(errors.Cause(err)) {
				return nil
			}
			return err
		}
		log.Infof("Engine got new owner %v", ec.controllerID)
	}

	if engine.DeletionTimestamp != nil {
		if err := ec.DeleteInstance(engine); err != nil {
			return errors.Wrapf(err, "failed to clean up the related engine instance before deleting engine %v", engine.Name)
		}
		return ec.ds.RemoveFinalizerForEngine(engine)
	}

	existingEngine := engine.DeepCopy()
	defer func() {
		// we're going to update engine assume things changes
		if err == nil && !reflect.DeepEqual(existingEngine.Status, engine.Status) {
			_, err = ec.ds.UpdateEngineStatus(engine)
		}
		// requeue if it's conflict
		if apierrors.IsConflict(errors.Cause(err)) {
			log.WithError(err).Debug("Requeue engine due to conflict")
			ec.enqueueEngine(engine)
			err = nil
		}
	}()

	syncReplicaAddressMap := false
	if len(engine.Spec.UpgradedReplicaAddressMap) != 0 && engine.Status.CurrentImage != engine.Spec.Image {
		if err := ec.Upgrade(engine, log); err != nil {
			// Engine live upgrade failure shouldn't block the following engine state update.
			log.WithError(err).Error("Failed to run engine live upgrade")
			// Sync replica address map as usual when the upgrade fails.
			syncReplicaAddressMap = true
		}
	} else if len(engine.Spec.UpgradedReplicaAddressMap) == 0 {
		syncReplicaAddressMap = true
	}
	if syncReplicaAddressMap && !reflect.DeepEqual(engine.Status.CurrentReplicaAddressMap, engine.Spec.ReplicaAddressMap) {
		log.Infof("Updating engine current replica address map to %+v", engine.Spec.ReplicaAddressMap)
		engine.Status.CurrentReplicaAddressMap = engine.Spec.ReplicaAddressMap
		// Make sure the CurrentReplicaAddressMap persist in the etcd before continue
		return nil
	}

	if err := ec.instanceHandler.ReconcileInstanceState(engine, &engine.Spec.InstanceSpec, &engine.Status.InstanceStatus); err != nil {
		return err
	}

	if engine.Status.CurrentState == longhorn.InstanceStateRunning {
		// we allow across monitoring temporarily due to migration case
		if !ec.isMonitoring(engine) {
			ec.startMonitoring(engine)
		} else if engine.Status.ReplicaModeMap != nil && engine.Spec.DesireState != longhorn.InstanceStateStopped {
			// If engine.Spec.DesireState == longhorn.InstanceStateStopped, we have likely already issued a command to
			// shut down the engine. It is potentially dangerous to attempt to communicate with it now, as a new engine
			// may start using its address.
			if err := ec.ReconcileEngineState(engine); err != nil {
				return err
			}
		}
	} else if ec.isMonitoring(engine) {
		// engine is not running
		ec.resetAndStopMonitoring(engine)
	}

	if err := ec.syncSnapshotCRs(engine); err != nil {
		return errors.Wrapf(err, "failed to sync with snapshot CRs for engine %v", engine.Name)
	}

	// Clean up CloneStatus for later retry
	if engine.Spec.RequestedDataSource == "" && failedCloneBefore(engine) {
		engine.Status.CloneStatus = nil
	}

	return nil
}

func failedCloneBefore(e *longhorn.Engine) bool {
	for _, status := range e.Status.CloneStatus {
		if status.State == engineapi.ProcessStateError {
			return true
		}
	}
	return false
}

func (ec *EngineController) enqueueEngine(obj interface{}) {
	key, err := controller.KeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %v", obj, err))
		return
	}

	ec.queue.Add(key)
}

func (ec *EngineController) enqueueInstanceManagerChange(obj interface{}) {
	im, isInstanceManager := obj.(*longhorn.InstanceManager)
	if !isInstanceManager {
		deletedState, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("received unexpected obj: %#v", obj))
			return
		}

		// use the last known state, to enqueue, dependent objects
		im, ok = deletedState.Obj.(*longhorn.InstanceManager)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("cannot convert DeletedFinalStateUnknown to InstanceManager object: %#v", deletedState.Obj))
			return
		}
	}

	imType, err := datastore.CheckInstanceManagerType(im)
	if err != nil || (imType != longhorn.InstanceManagerTypeEngine && imType != longhorn.InstanceManagerTypeAllInOne) {
		return
	}

	engineMap := map[string]*longhorn.Engine{}

	es, err := ec.ds.ListEnginesRO()
	if err != nil {
		ec.logger.WithError(err).Warn("Failed to list engines")
	}
	for _, e := range es {
		// when attaching, instance manager name is not available
		// when detaching, node ID is not available
		if e.Spec.NodeID == im.Spec.NodeID || e.Status.InstanceManagerName == im.Name {
			engineMap[e.Name] = e
		}
	}

	for _, e := range engineMap {
		ec.enqueueEngine(e)
	}
}

func (ec *EngineController) CreateInstance(obj interface{}) (*longhorn.InstanceProcess, error) {
	e, ok := obj.(*longhorn.Engine)
	if !ok {
		return nil, fmt.Errorf("invalid object for engine process creation: %v", obj)
	}
	if e.Spec.VolumeName == "" || e.Spec.NodeID == "" {
		return nil, fmt.Errorf("missing parameters for engine instance creation: %v", e)
	}
	frontend := e.Spec.Frontend
	if e.Spec.DisableFrontend {
		frontend = longhorn.VolumeFrontendEmpty
	}

	im, err := ec.ds.GetInstanceManagerByInstanceRO(obj)
	if err != nil {
		return nil, err
	}

	c, err := engineapi.NewInstanceManagerClient(im, false)
	if err != nil {
		return nil, err
	}
	defer func(c io.Closer) {
		if closeErr := c.Close(); closeErr != nil {
			ec.logger.WithError(closeErr).Warn("Failed to close instance manager client")
		}
	}(c)

	engineReplicaTimeout, err := ec.ds.GetSettingAsInt(types.SettingNameEngineReplicaTimeout)
	if err != nil {
		return nil, err
	}

	fileSyncHTTPClientTimeout, err := ec.ds.GetSettingAsInt(types.SettingNameReplicaFileSyncHTTPClientTimeout)
	if err != nil {
		return nil, err
	}

	v, err := ec.ds.GetVolume(e.Spec.VolumeName)
	if err != nil {
		return nil, err
	}

	cliAPIVersion, err := ec.ds.GetDataEngineImageCLIAPIVersion(e.Spec.Image, e.Spec.DataEngine)
	if err != nil {
		return nil, err
	}

	instanceManagerPod, err := ec.ds.GetPod(im.Name)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get pod for instance manager %v", im.Name)
	}

	instanceManagerStorageIP := ec.ds.GetStorageIPFromPod(instanceManagerPod)

	return c.EngineInstanceCreate(&engineapi.EngineInstanceCreateRequest{
		Engine:                           e,
		VolumeFrontend:                   frontend,
		EngineReplicaTimeout:             engineReplicaTimeout,
		ReplicaFileSyncHTTPClientTimeout: fileSyncHTTPClientTimeout,
		DataLocality:                     v.Spec.DataLocality,
		EngineCLIAPIVersion:              cliAPIVersion,
		UpgradeRequired:                  false,
		InitiatorAddress:                 instanceManagerStorageIP,
		TargetAddress:                    instanceManagerStorageIP,
	})
}

func (ec *EngineController) DeleteInstance(obj interface{}) (err error) {
	e, ok := obj.(*longhorn.Engine)
	if !ok {
		return fmt.Errorf("invalid object for engine process deletion: %v", obj)
	}

	log := getLoggerForEngine(ec.logger, e)
	var im *longhorn.InstanceManager

	// Not assigned or not updated, try best to delete
	if e.Status.InstanceManagerName == "" {
		if e.Spec.NodeID == "" {
			log.Warn("Engine does not set instance manager name and node ID, will skip the actual instance deletion")
			return nil
		}
		im, err = ec.ds.GetInstanceManagerByInstance(obj)
		if err != nil {
			log.WithError(err).Warn("Failed to detect instance manager for engine, will skip the actual instance deletion")
			return nil
		}
		log.Infof("Cleaning up the process for engine in instance manager %v", im.Name)
	} else {
		im, err = ec.ds.GetInstanceManager(e.Status.InstanceManagerName)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return err
			}
			// The related node may be directly deleted.
			log.Warnf("The engine instance manager %v is gone during the engine instance %v deletion. Will do nothing for the deletion", e.Status.InstanceManagerName, e.Name)
			return nil
		}
	}

	isRWXVolume, err := ec.ds.IsRegularRWXVolume(e.Spec.VolumeName)
	if err != nil {
		return err
	}

	if shouldSkip, skipReason := shouldSkipEngineDeletion(im.Status.CurrentState, isRWXVolume); shouldSkip {
		log.Infof("Skipping deleting engine %v since %s", e.Name, skipReason)
		return nil
	}

	isDelinquent, err := ec.ds.IsNodeDelinquent(im.Spec.NodeID, e.Spec.VolumeName)
	if err != nil {
		return err
	}

	log.Info("Deleting engine instance")

	defer func() {
		if err != nil {
			log.WithError(err).Warnf("Failed to delete engine %v", e.Name)
			if canIgnore, ignoreReason := canIgnoreEngineDeletionFailure(im.Status.CurrentState, isRWXVolume,
				isDelinquent); canIgnore {
				log.Warnf("Ignored the failure to delete engine %v because %s", e.Name, ignoreReason)
				err = nil
			}
		}
	}()

	// For the engine instance in instance manager v0.7.0, we need to use the cmdline to delete the instance
	// and stop the iscsi
	if im.Status.APIVersion == engineapi.IncompatibleInstanceManagerAPIVersion {
		url := imutil.GetURL(im.Status.IP, engineapi.InstanceManagerProcessManagerServiceDefaultPort)
		args := []string{"--url", url, "engine", "delete", "--name", e.Name}

		execute := lhexec.NewExecutor().Execute
		deprecatedIMBinary := engineapi.GetDeprecatedInstanceManagerBinary(e.Status.CurrentImage)
		_, err = execute([]string{}, deprecatedIMBinary, args, lhtypes.ExecuteNoTimeout)
		if err != nil && !types.ErrorIsNotFound(err) {
			return err
		}

		// Directly remove the instance from the map. Best effort.
		delete(im.Status.Instances, e.Name) // nolint: staticcheck
		if _, err := ec.ds.UpdateInstanceManagerStatus(im); err != nil {
			return err
		}
		return nil
	}

	c, err := engineapi.NewInstanceManagerClient(im, true)
	if err != nil {
		return err
	}
	defer func(c io.Closer) {
		if closeErr := c.Close(); closeErr != nil {
			ec.logger.WithError(closeErr).Warn("Failed to close instance manager client")
		}
	}(c)

	err = c.InstanceDelete(e.Spec.DataEngine, e.Name, "", string(longhorn.InstanceManagerTypeEngine), "", true)
	if err != nil && !types.ErrorIsNotFound(err) {
		return err
	}

	return nil
}

func (ec *EngineController) GetInstance(obj interface{}) (*longhorn.InstanceProcess, error) {
	e, ok := obj.(*longhorn.Engine)
	if !ok {
		return nil, fmt.Errorf("invalid object for engine instance get: %v", obj)
	}

	var (
		im  *longhorn.InstanceManager
		err error
	)
	if e.Status.InstanceManagerName == "" {
		im, err = ec.ds.GetInstanceManagerByInstanceRO(obj)
		if err != nil {
			return nil, err
		}
	} else {
		im, err = ec.ds.GetInstanceManagerRO(e.Status.InstanceManagerName)
		if err != nil {
			return nil, err
		}
	}
	c, err := engineapi.NewInstanceManagerClient(im, false)
	if err != nil {
		return nil, err
	}
	defer func(c io.Closer) {
		if closeErr := c.Close(); closeErr != nil {
			ec.logger.WithError(closeErr).Warn("Failed to close instance manager client")
		}
	}(c)

	return c.InstanceGet(e.Spec.DataEngine, e.Name, string(longhorn.InstanceManagerTypeEngine))
}

func (ec *EngineController) LogInstance(ctx context.Context, obj interface{}) (*engineapi.InstanceManagerClient, *imapi.LogStream, error) {
	e, ok := obj.(*longhorn.Engine)
	if !ok {
		return nil, nil, fmt.Errorf("invalid object for engine instance log: %v", obj)
	}

	im, err := ec.ds.GetInstanceManagerRO(e.Status.InstanceManagerName)
	if err != nil {
		return nil, nil, err
	}

	c, err := engineapi.NewInstanceManagerClient(im, false)
	if err != nil {
		return nil, nil, err
	}

	// TODO: #2441 refactor this when we do the resource monitoring refactor
	stream, err := c.InstanceLog(ctx, e.Spec.DataEngine, e.Name, string(longhorn.InstanceManagerTypeEngine))
	return c, stream, err
}

func (ec *EngineController) isMonitoring(e *longhorn.Engine) bool {
	ec.engineMonitorMutex.RLock()
	defer ec.engineMonitorMutex.RUnlock()

	_, ok := ec.engineMonitorMap[e.Name]
	return ok
}

func (ec *EngineController) startMonitoring(e *longhorn.Engine) {
	stopCh := make(chan struct{})
	monitorVoluntaryStopCh := make(chan struct{})
	monitor := &EngineMonitor{
		logger:                 ec.logger.WithField("engine", e.Name),
		Name:                   e.Name,
		namespace:              e.Namespace,
		ds:                     ec.ds,
		eventRecorder:          ec.eventRecorder,
		engines:                ec.engines,
		stopCh:                 stopCh,
		monitorVoluntaryStopCh: monitorVoluntaryStopCh,
		expansionBackoff:       flowcontrol.NewBackOff(time.Second*10, time.Minute*5),
		restoreBackoff:         flowcontrol.NewBackOff(time.Second*10, restoreMaxInterval),
		controllerID:           ec.controllerID,
		proxyConnCounter:       ec.proxyConnCounter,
		restoringCounter:       ec.restoringCounter,
		restoringCounterMutex:  ec.restoringCounterMutex,
		sizeUpdateLimiter:      rate.NewLimiter(rate.Every(sizeUpdateLimit), sizeUpdateBurst),
	}

	ec.engineMonitorMutex.Lock()
	defer ec.engineMonitorMutex.Unlock()

	if _, ok := ec.engineMonitorMap[e.Name]; ok {
		return
	}
	ec.engineMonitorMap[e.Name] = stopCh

	go monitor.Run()
	go func() {
		<-monitorVoluntaryStopCh
		ec.engineMonitorMutex.Lock()
		delete(ec.engineMonitorMap, e.Name)
		ec.engineMonitorMutex.Unlock()
	}()
}

func (ec *EngineController) resetAndStopMonitoring(e *longhorn.Engine) {
	if _, err := ec.ds.ResetMonitoringEngineStatus(e); err != nil {
		utilruntime.HandleError(errors.Wrapf(err, "failed to update engine %v to stop monitoring", e.Name))
		// better luck next time
		return
	}

	ec.stopMonitoring(e.Name)

}

func (ec *EngineController) stopMonitoring(engineName string) {
	ec.engineMonitorMutex.Lock()
	defer ec.engineMonitorMutex.Unlock()

	stopCh, ok := ec.engineMonitorMap[engineName]
	if !ok {
		return
	}

	select {
	case <-stopCh:
		// stopCh channel is already closed
	default:
		close(stopCh)
	}

}

func (m *EngineMonitor) Run() {
	m.logger.Info("Starting monitoring engine")
	defer func() {
		if err := m.acquireRestoringCounter(false); err != nil {
			m.logger.WithError(err).Error("Failed to unacquire restoring counter")
		}
		m.logger.Info("Stopping monitoring engine")
		close(m.monitorVoluntaryStopCh)
	}()

	ticker := time.NewTicker(EnginePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if needStop := m.sync(); needStop {
				return
			}
		case <-m.stopCh:
			return
		}
	}
}

func (m *EngineMonitor) sync() bool {
	for count := 0; count < EngineMonitorConflictRetryCount; count++ {
		engine, err := m.ds.GetEngine(m.Name)
		if err != nil {
			if datastore.ErrorIsNotFound(err) {
				m.logger.Warn("Stopping monitoring because the engine no longer exists")
				return true
			}
			utilruntime.HandleError(errors.Wrapf(err, "failed to get engine %v for monitoring", m.Name))
			return false
		}

		if engine.Status.OwnerID != m.controllerID {
			m.logger.Warnf("Stopping monitoring the engine on this node (%v) because the engine has new ownerID %v", m.controllerID, engine.Status.OwnerID)
			return true
		}

		// engine is maybe starting
		if engine.Status.CurrentState != longhorn.InstanceStateRunning {
			return false
		}

		// engine is upgrading
		if engine.Status.CurrentImage != engine.Spec.Image || len(engine.Spec.UpgradedReplicaAddressMap) != 0 {
			return false
		}

		if err := m.refresh(engine); err == nil || !apierrors.IsConflict(errors.Cause(err)) {
			utilruntime.HandleError(errors.Wrapf(err, "failed to update status for engine %v", m.Name))
			break
		}
		// Retry if the error is due to conflict
	}

	return false
}

func (m *EngineMonitor) refresh(engine *longhorn.Engine) error {
	existingEngine := engine.DeepCopy()

	addressReplicaMap := map[string]string{}
	for replica, address := range engine.Status.CurrentReplicaAddressMap {
		if addressReplicaMap[address] != "" {
			return fmt.Errorf("invalid ReplicaAddressMap: duplicate addresses")
		}
		addressReplicaMap[address] = replica
	}

	engineCliClient, err := GetBinaryClientForEngine(engine, m.engines, engine.Status.CurrentImage)
	if err != nil {
		return err
	}

	engineClientProxy, err := engineapi.GetCompatibleClient(engine, engineCliClient, m.ds, m.logger, m.proxyConnCounter)
	if err != nil {
		return err
	}
	defer engineClientProxy.Close()

	replicaURLModeMap, err := engineClientProxy.ReplicaList(engine)
	if err != nil {
		return err
	}

	currentReplicaModeMap := map[string]longhorn.ReplicaMode{}
	currentReplicaTransitionTimeMap := map[string]string{}
	for url, r := range replicaURLModeMap {
		addr := engineapi.GetAddressFromBackendReplicaURL(url)
		replica, exists := addressReplicaMap[addr]
		if !exists {
			// we have a entry doesn't exist in our spec
			replica = unknownReplicaPrefix + url

			// The unknown replica will remove and record the event during
			// ReconcileEngineState.
			// https://github.com/longhorn/longhorn/issues/4120
			currentReplicaModeMap[replica] = r.Mode
			m.logger.Warnf("Found unknown replica %v in the Replica URL Mode Map", addr)
			continue
		}

		currentReplicaModeMap[replica] = r.Mode

		if engine.Status.ReplicaModeMap == nil {
			// We are constructing the ReplicaModeMap for the first time. Construct the ReplicaTransitionTimeMap
			// alongside it.
			currentReplicaTransitionTimeMap[replica] = util.Now()
		} else {
			if r.Mode != engine.Status.ReplicaModeMap[replica] {
				switch r.Mode {
				case longhorn.ReplicaModeERR:
					m.eventRecorder.Eventf(engine, corev1.EventTypeWarning, constant.EventReasonFaulted, "Detected replica %v (%v) in error", replica, addr)
					currentReplicaTransitionTimeMap[replica] = util.Now()
				case longhorn.ReplicaModeWO:
					m.eventRecorder.Eventf(engine, corev1.EventTypeNormal, constant.EventReasonRebuilding, "Detected rebuilding replica %v (%v)", replica, addr)
					currentReplicaTransitionTimeMap[replica] = util.Now()
				case longhorn.ReplicaModeRW:
					m.eventRecorder.Eventf(engine, corev1.EventTypeNormal, constant.EventReasonRebuilt, "Detected replica %v (%v) has been rebuilt", replica, addr)
					currentReplicaTransitionTimeMap[replica] = util.Now()
				default:
					m.logger.Errorf("Invalid engine replica mode %v", r.Mode)
				}
			} else {
				oldTime, ok := engine.Status.ReplicaTransitionTimeMap[replica]
				if !ok {
					m.logger.Errorf("BUG: Replica %v (%v) was previously in mode %v but transition time was not recorded", replica, addr, engine.Status.ReplicaModeMap[replica])
					currentReplicaTransitionTimeMap[replica] = util.Now()
				} else {
					currentReplicaTransitionTimeMap[replica] = oldTime
				}
			}
		}
	}
	engine.Status.ReplicaModeMap = currentReplicaModeMap
	engine.Status.ReplicaTransitionTimeMap = currentReplicaTransitionTimeMap

	snapshots, err := engineClientProxy.SnapshotList(engine)
	if err != nil {
		engine.Status.SnapshotsError = err.Error()
	} else {
		engine.Status.Snapshots = snapshots
		engine.Status.SnapshotsError = ""
	}

	// TODO: find a more advanced way to handle invocations for incompatible running engines
	im, err := m.ds.GetInstanceManagerRO(engine.Status.InstanceManagerName)
	if err != nil {
		return err
	}

	cliAPIVersion, err := m.ds.GetDataEngineImageCLIAPIVersion(engine.Status.CurrentImage, engine.Spec.DataEngine)
	if err != nil {
		return err
	}

	if (types.IsDataEngineV1(engine.Spec.DataEngine) && cliAPIVersion >= engineapi.CLIAPIMinVersionForExistingEngineBeforeUpgrade) ||
		types.IsDataEngineV2(engine.Spec.DataEngine) {
		volumeInfo, err := engineClientProxy.VolumeGet(engine)
		if err != nil {
			return err
		}
		endpoint, err := engineapi.GetEngineEndpoint(volumeInfo, engine.Status.IP)
		if err != nil {
			return err
		}
		engine.Status.Endpoint = endpoint

		if volumeInfo.LastExpansionError != "" && volumeInfo.LastExpansionFailedAt != "" &&
			(engine.Status.LastExpansionError != volumeInfo.LastExpansionError ||
				engine.Status.LastExpansionFailedAt != volumeInfo.LastExpansionFailedAt) {
			isLatestErrorInfo := true
			if expansionFailureTime, err := time.Parse(time.RFC3339Nano, volumeInfo.LastExpansionFailedAt); err == nil && m.expansionUpdateTime.After(expansionFailureTime) {
				isLatestErrorInfo = false
			}
			if isLatestErrorInfo {
				m.eventRecorder.Eventf(engine, corev1.EventTypeWarning, constant.EventReasonFailedExpansion,
					"Engine failed or partially failed to expand the size at %v: %v", volumeInfo.LastExpansionFailedAt, volumeInfo.LastExpansionError)
				engine.Status.LastExpansionError = volumeInfo.LastExpansionError
				engine.Status.LastExpansionFailedAt = volumeInfo.LastExpansionFailedAt
				m.expansionUpdateTime = time.Now()
				m.expansionBackoff.Next(engine.Name, time.Now())
			}
		}
		if engine.Status.CurrentSize != 0 && engine.Status.CurrentSize != volumeInfo.Size {
			m.eventRecorder.Eventf(engine, corev1.EventTypeNormal, constant.EventReasonSucceededExpansion,
				"Engine successfully expand size from %v to %v", engine.Status.CurrentSize, volumeInfo.Size)
			m.expansionUpdateTime = time.Now()
		}
		engine.Status.CurrentSize = volumeInfo.Size
		engine.Status.IsExpanding = volumeInfo.IsExpanding

		if engine.Status.Endpoint == "" && !engine.Spec.DisableFrontend && engine.Spec.Frontend != longhorn.VolumeFrontendEmpty {
			m.logger.Infof("Starting frontend %v", engine.Spec.Frontend)
			if err := engineClientProxy.VolumeFrontendStart(engine); err != nil {
				return errors.Wrapf(err, "failed to start frontend %v", engine.Spec.Frontend)
			}
		}

		// The rebuild failure will be handled by ec.startRebuilding()
		rebuildStatus, err := engineClientProxy.ReplicaRebuildStatus(engine)
		if err != nil {
			return err
		}

		if err := m.checkAndApplyRebuildQoS(engine, engineClientProxy, rebuildStatus); err != nil {
			return err
		}
		engine.Status.RebuildStatus = rebuildStatus

		// It's meaningless to sync the trim related field for old engines or engines in old engine instance managers
		if cliAPIVersion >= 7 && im.Status.APIVersion >= 3 {
			// Check and correct flag UnmapMarkSnapChainRemoved for the engine and replicas
			engine.Status.UnmapMarkSnapChainRemovedEnabled = volumeInfo.UnmapMarkSnapChainRemoved
			if engine.Spec.UnmapMarkSnapChainRemovedEnabled != volumeInfo.UnmapMarkSnapChainRemoved {
				if err := engineClientProxy.VolumeUnmapMarkSnapChainRemovedSet(engine); err != nil {
					return errors.Wrapf(err, "failed to correct flag UnmapMarkSnapChainRemoved from %v to %v",
						volumeInfo.UnmapMarkSnapChainRemoved, engine.Spec.UnmapMarkSnapChainRemovedEnabled)
				}
			}
		}

		if cliAPIVersion >= 10 && im.Status.APIVersion >= 5 {
			engine.Status.SnapshotMaxCount = volumeInfo.SnapshotMaxCount
			if engine.Spec.SnapshotMaxCount != volumeInfo.SnapshotMaxCount {
				logrus.Infof("Correcting flag SnapshotMaxCount from %d to %d", volumeInfo.SnapshotMaxCount, engine.Spec.SnapshotMaxCount)
				if err := engineClientProxy.VolumeSnapshotMaxCountSet(engine); err != nil {
					return errors.Wrapf(err, "failed to correct flag SnapshotMaxCount from %d to %d",
						volumeInfo.SnapshotMaxCount, engine.Spec.SnapshotMaxCount)
				}
			}

			engine.Status.SnapshotMaxSize = volumeInfo.SnapshotMaxSize
			if engine.Spec.SnapshotMaxSize != volumeInfo.SnapshotMaxSize {
				logrus.Infof("Correcting flag SnapshotMaxSize from %d to %d", volumeInfo.SnapshotMaxSize, engine.Spec.SnapshotMaxSize)
				if err := engineClientProxy.VolumeSnapshotMaxSizeSet(engine); err != nil {
					return errors.Wrapf(err, "failed to correct flag SnapshotMaxSize from %d to %d",
						volumeInfo.SnapshotMaxSize, engine.Spec.SnapshotMaxSize)
				}
			}
		}
	} else {
		// For incompatible running engine, the current size is always `engine.Spec.VolumeSize`.
		engine.Status.CurrentSize = engine.Spec.VolumeSize
		engine.Status.RebuildStatus = map[string]*longhorn.RebuildStatus{}
	}

	// TODO: Check if the purge failure is handled somewhere else
	purgeStatus, err := engineClientProxy.SnapshotPurgeStatus(engine)
	if err != nil {
		m.logger.WithError(err).Warn("Failed to get snapshot purge status")
	} else {
		engine.Status.PurgeStatus = purgeStatus
	}

	removeInvalidEngineOpStatus(engine)

	// Make sure the engine object is updated before engineapi calls.
	needStatusUpdate, rateLimited := m.needStatusUpdate(existingEngine, engine)
	if needStatusUpdate && (!rateLimited || m.sizeUpdateLimiter.Tokens() >= 1) {
		if engine, err = m.ds.UpdateEngineStatus(engine); err != nil {
			return err
		}
		if rateLimited {
			// Normally we would operate a Limiter by first obtaining a token, then taking an action. Here, we do not
			// want to obtain a token unless the update succeeds. Otherwise the next attempt at an update (probably in a
			// few milliseconds), will not be allowed. We might be able to refactor the engine controller so that update
			// retries happen here instead of at the caller, but it's fine to operate the Limiter this way, since there
			// is only one thread that uses it.
			if obtainedToken := m.sizeUpdateLimiter.Allow(); !obtainedToken {
				m.logger.Warnf("BUG: Size update bypassed rate limiting")
			}
		}
		existingEngine = engine.DeepCopy()
	}

	requireExpansion, err := IsValidForExpansion(engine, cliAPIVersion, im.Status.APIVersion)
	if err != nil {
		engine.Status.LastExpansionError = err.Error()
		engine.Status.LastExpansionFailedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if requireExpansion {
		// Cannot continue to start restoration if expansion is not complete
		if m.expansionBackoff.IsInBackOffSinceUpdate(engine.Name, time.Now()) {
			m.logger.Debug("Cannot start engine expansion since it is in the back-off window")
		} else {
			m.logger.Infof("Starting engine expansion from %v to %v", engine.Status.CurrentSize, engine.Spec.VolumeSize)
			// The error info and the backoff interval will be updated later.
			m.expansionUpdateTime = time.Now()
			if err := engineClientProxy.VolumeExpand(engine); err != nil {
				return err
			}
		}
		return nil
	}
	// This means expansion is complete/unnecessary, and it's safe to clean up the backoff entry as well as the error info if exists.
	if engine.Spec.VolumeSize == engine.Status.CurrentSize {
		m.expansionBackoff.DeleteEntry(engine.Name)
		m.expansionUpdateTime = time.Now()
		engine.Status.LastExpansionError = ""
		engine.Status.LastExpansionFailedAt = ""
	}

	rsMap, err := engineClientProxy.BackupRestoreStatus(engine)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			m.logger.WithError(err).Warn("Engine Monitor")
			return
		}

		engine.Status.RestoreStatus = rsMap

		removeInvalidEngineOpStatus(engine)

		isBackupRestoreCompleted := existingEngine.Status.LastRestoredBackup != engine.Status.LastRestoredBackup
		if isBackupRestoreCompleted || isBackupRestoreFailed(engine.Status.RestoreStatus) {
			if err := m.acquireRestoringCounter(false); err != nil {
				m.logger.WithError(err).Warn("Engine Monitor: Failed to unacquire restoring counter")
			}
		}

		// Ignore size change here to maintain rate limiting. (If we wanted to update status based on a size change,
		// we already did so above.)
		if needStatusUpdateBesidesSize(&existingEngine.Status, &engine.Status) {
			e, err := m.ds.UpdateEngineStatus(engine)
			if err != nil {
				m.logger.WithError(err).Warn("Engine Monitor: Failed to update engine status")
				return
			}
			engine = e
		}
	}()

	needRestore, err := preRestoreCheckAndSync(m.logger, engine, rsMap, addressReplicaMap, cliAPIVersion, m.ds, engineClientProxy)
	if err != nil {
		return err
	}
	// Incremental restoration will implicitly expand the DR volume once the backup volume is expanded
	if needRestore {
		if m.restoreBackoff.IsInBackOffSinceUpdate(engine.Name, time.Now()) {
			m.logger.Debug("Cannot restore the backup for engine since it is still in the backoff window")
			return nil
		}

		volume, err := m.ds.GetVolumeRO(engine.Spec.VolumeName)
		if err != nil {
			return errors.Wrapf(err, "failed to get volume %v for restoring counter", engine.Spec.VolumeName)
		}
		isDRVolume := volume.Status.IsStandby
		if !isDRVolume {
			err := m.acquireRestoringCounter(true)
			if err != nil {
				m.logger.WithError(err).Warn("Failed to acquire restore counter, retry later")
				m.restoreBackoff.Next(engine.Name, time.Now())
				return nil
			}
		}

		if err = m.restoreBackup(engine, rsMap, cliAPIVersion, engineClientProxy); err != nil {
			m.restoreBackoff.DeleteEntry(engine.Name)
			if err := m.acquireRestoringCounter(false); err != nil {
				m.logger.WithError(err).Warn("Failed to unacquire restoring counter")
			}
			return err
		}
	}

	var snapshotCloneStatusMap map[string]*longhorn.SnapshotCloneStatus
	if cliAPIVersion >= engineapi.CLIVersionFive {
		if snapshotCloneStatusMap, err = engineClientProxy.SnapshotCloneStatus(engine); err != nil {
			return err
		}
	}

	engine.Status.CloneStatus = snapshotCloneStatusMap

	needClone, err := preCloneCheck(engine)
	if err != nil {
		return err
	}
	if needClone {
		if err = cloneSnapshot(engine, engineClientProxy, m.ds); err != nil {
			return err
		}
	}

	return nil
}

func (m *EngineMonitor) checkAndApplyRebuildQoS(engine *longhorn.Engine, engineClientProxy engineapi.EngineClientProxy, rebuildStatus map[string]*longhorn.RebuildStatus) error {
	if !types.IsDataEngineV2(engine.Spec.DataEngine) {
		return nil
	}

	expectedQoSValue, err := m.getEffectiveRebuildQoS(engine)
	if err != nil {
		return err
	}

	for replica, newStatus := range rebuildStatus {
		if newStatus == nil {
			continue
		}

		var appliedQoS int64
		if oldStatus, exists := engine.Status.RebuildStatus[replica]; exists && oldStatus != nil {
			appliedQoS = oldStatus.AppliedRebuildingMBps
		}

		if appliedQoS == expectedQoSValue {
			newStatus.AppliedRebuildingMBps = appliedQoS
			continue
		}

		if !newStatus.IsRebuilding {
			continue
		}

		if err := engineClientProxy.ReplicaRebuildQosSet(engine, expectedQoSValue); err != nil {
			m.logger.WithError(err).Warnf("[qos] Failed to set QoS for volume %s, replica %s", engine.Spec.VolumeName, replica)
			continue
		}
		newStatus.AppliedRebuildingMBps = expectedQoSValue
	}
	return nil
}

func (m *EngineMonitor) getEffectiveRebuildQoS(engine *longhorn.Engine) (int64, error) {
	globalQoS, err := m.ds.GetSettingAsInt(types.SettingNameV2DataEngineRebuildingMbytesPerSecond)
	if err != nil {
		return 0, err
	}

	volume, err := m.ds.GetVolumeRO(engine.Spec.VolumeName)
	if err != nil {
		return 0, err
	}

	if volume.Spec.RebuildingMbytesPerSecond > 0 {
		return volume.Spec.RebuildingMbytesPerSecond, nil
	}

	return globalQoS, nil
}

func isBackupRestoreFailed(rsMap map[string]*longhorn.RestoreStatus) bool {
	for _, status := range rsMap {
		if status.IsRestoring {
			break
		}

		if status.Error != "" {
			return true
		}
	}
	return false
}

func (m *EngineMonitor) acquireRestoringCounter(acquire bool) error {
	m.restoringCounterMutex.Lock()
	defer m.restoringCounterMutex.Unlock()

	if !acquire {
		if m.restoringCounterAcquired {
			m.restoringCounter.DecreaseCount()
			m.restoringCounterAcquired = false
		}
		return nil
	}

	if isReachedLimit, err := m.isReachedConcurrentVolumeBackupRestoreLimit(); err != nil {
		return errors.Wrap(err, "failed to check concurrent volume backup restore limit")

	} else if isReachedLimit {
		return fmt.Errorf("reached the concurrent volume backup restore limit of %v", types.SettingNameConcurrentBackupRestorePerNodeLimit)
	}

	if m.restoringCounterAcquired {
		return nil
	}
	m.restoringCounterAcquired = true
	m.restoringCounter.IncreaseCount()
	return nil
}

func (ec *EngineController) syncSnapshotCRs(engine *longhorn.Engine) error {
	log := ec.logger.WithField("engine", engine.Name)

	vol, err := ec.ds.GetVolumeRO(engine.Spec.VolumeName)
	if err != nil {
		return err
	}
	if util.IsVolumeMigrating(vol) {
		return nil
	}

	snapshotCRs, err := ec.ds.ListVolumeSnapshotsRO(engine.Spec.VolumeName)
	if err != nil {
		return err
	}
	if engine.Status.SnapshotsError != "" {
		return nil
	}

	for snapName, snapCR := range snapshotCRs {
		requestCreateNewSnapshot := snapCR.Spec.CreateSnapshot
		alreadyCreatedBefore := snapCR.Status.CreationTime != ""
		if _, ok := engine.Status.Snapshots[snapName]; !ok && (!requestCreateNewSnapshot || alreadyCreatedBefore) && snapCR.DeletionTimestamp == nil {
			log.Infof("Deleting snapshot CR for the snapshot %v", snapName)
			if err := ec.ds.DeleteSnapshot(snapName); err != nil && !apierrors.IsNotFound(err) {
				utilruntime.HandleError(fmt.Errorf("syncSnapshotCRs: failed to delete snapshot CR %v: %v", snapCR.Name, err))
			}
		}
	}

	volume, err := ec.ds.GetVolumeRO(engine.Spec.VolumeName)
	if err != nil {
		return err
	}

	for snapName := range engine.Status.Snapshots {
		// Don't create snapshot CR for the volume-head snapshot
		if snapName == etypes.VolumeHeadName {
			continue
		}

		if _, ok := snapshotCRs[snapName]; !ok {
			// snapshotCR does not exist, create a new one
			snapCR := &longhorn.Snapshot{
				ObjectMeta: metav1.ObjectMeta{
					Name: snapName,
				},
				Spec: longhorn.SnapshotSpec{
					Volume:         volume.Name,
					CreateSnapshot: false,
				},
			}
			log.Infof("Creating snapshot CR for the snapshot %v", snapName)
			if _, err := ec.ds.CreateSnapshot(snapCR); err != nil && !apierrors.IsAlreadyExists(err) {
				utilruntime.HandleError(fmt.Errorf("syncSnapshotCRs: failed to create snapshot CR %v: %v", snapCR.Name, err))
			}
		}
	}

	return nil
}

func IsValidForExpansion(engine *longhorn.Engine, cliAPIVersion, imAPIVersion int) (bool, error) {
	if engine.Status.IsExpanding {
		return false, nil
	}
	if engine.Spec.VolumeSize == engine.Status.CurrentSize {
		return false, nil
	}
	if engine.Spec.VolumeSize < engine.Status.CurrentSize {
		return false, fmt.Errorf("the expected size %v of engine %v should not be smaller than the current size %v", engine.Spec.VolumeSize, engine.Name, engine.Status.CurrentSize)
	}

	if cliAPIVersion < engineapi.CLIAPIMinVersionForExistingEngineBeforeUpgrade {
		return false, nil
	}

	if !engineapi.IsEndpointTGTBlockDev(engine.Status.Endpoint) {
		return true, nil
	}
	if cliAPIVersion < 7 {
		return false, fmt.Errorf("failed to do online expansion for the old engine %v with cli API version %v", engine.Name, cliAPIVersion)
	}
	if imAPIVersion < 3 {
		return false, fmt.Errorf("failed do online expansion for the engine %v in the instance manager with API version %v", engine.Name, imAPIVersion)
	}

	return true, nil
}

func preRestoreCheckAndSync(log logrus.FieldLogger, engine *longhorn.Engine,
	rsMap map[string]*longhorn.RestoreStatus, addressReplicaMap map[string]string,
	cliAPIVersion int, ds *datastore.DataStore, engineClientProxy engineapi.EngineClientProxy) (needRestore bool, err error) {
	defer func() {
		if err != nil {
			err = errors.Wrapf(err, "failed pre-restore check for engine %v", engine.Name)
			// Need to manually update the restore status if the the check fails
			for _, status := range rsMap {
				status.Error = err.Error()
			}
			needRestore = false
		}
	}()

	if rsMap == nil {
		return false, nil
	}
	if cliAPIVersion < engineapi.CLIVersionFour {
		isRestoring, isConsensual := syncWithRestoreStatusForCompatibleEngine(log, engine, rsMap)
		if isRestoring || !isConsensual || engine.Spec.RequestedBackupRestore == "" || engine.Spec.RequestedBackupRestore == engine.Status.LastRestoredBackup {
			return false, nil
		}
	} else {
		if !syncWithRestoreStatus(log, engine, rsMap, addressReplicaMap, engineClientProxy) {
			return false, nil
		}
	}

	if engine.Spec.BackupVolume == "" {
		return false, fmt.Errorf("backup volume is empty for backup restoration of engine %v", engine.Name)
	}

	if cliAPIVersion >= engineapi.CLIAPIMinVersionForExistingEngineBeforeUpgrade {
		return checkSizeBeforeRestoration(log, engine, ds)
	}

	return true, nil
}

func syncWithRestoreStatus(log logrus.FieldLogger, engine *longhorn.Engine, rsMap map[string]*longhorn.RestoreStatus,
	addressReplicaMap map[string]string, engineClientProxy engineapi.EngineClientProxy) bool {
	for _, status := range engine.Status.PurgeStatus {
		if status.IsPurging {
			return false
		}
	}

	for _, status := range rsMap {
		if status.Error != "" {
			log.WithError(errors.New(status.Error)).Warn("Waiting for the restore error handling before the restore invocation")
			return false
		}
	}

	allReplicasAreRestoring := true
	isConsensual := true
	lastRestoredInitialized := false
	lastRestored := ""
	for url, status := range rsMap {
		if !status.IsRestoring {
			allReplicasAreRestoring = false

			// Verify the rebuilding replica after the restore complete. This call will set the replica mode to RW.
			if status.LastRestored != "" {
				replicaName := addressReplicaMap[engineapi.GetAddressFromBackendReplicaURL(url)]
				if mode, exists := engine.Status.ReplicaModeMap[replicaName]; exists && mode == longhorn.ReplicaModeWO {
					log.Infof("Verifying the rebuild of replica %v after restore completion", url)
					if err := engineClientProxy.ReplicaRebuildVerify(engine, replicaName, url); err != nil {
						log.WithError(err).Errorf("Failed to verify the rebuild of replica %v after restore completion", url)
						engine.Status.ReplicaModeMap[url] = longhorn.ReplicaModeERR
						return false
					}
				}
			}
		}
		if !lastRestoredInitialized {
			lastRestored = status.LastRestored
			lastRestoredInitialized = true
		}
		if status.LastRestored != lastRestored {
			isConsensual = false
		}
	}
	if isConsensual {
		if engine.Status.LastRestoredBackup != lastRestored {
			log.Infof("Updating last restored backup from %v to %v", engine.Status.LastRestoredBackup, lastRestored)
		}
		engine.Status.LastRestoredBackup = lastRestored
	} else {
		if engine.Status.LastRestoredBackup != "" {
			log.Infof("Cleaning up the field LastRestoredBackup %v due to the inconsistency. Maybe it's caused by replica rebuilding", engine.Status.LastRestoredBackup)
		}
		engine.Status.LastRestoredBackup = ""
	}

	if engine.Spec.RequestedBackupRestore != "" && engine.Spec.RequestedBackupRestore != engine.Status.LastRestoredBackup && !allReplicasAreRestoring {
		return true
	}
	return false
}

func syncWithRestoreStatusForCompatibleEngine(log logrus.FieldLogger, engine *longhorn.Engine, rsMap map[string]*longhorn.RestoreStatus) (bool, bool) {
	isRestoring := false
	isConsensual := true
	lastRestored := ""
	for _, status := range rsMap {
		if status.IsRestoring {
			isRestoring = true
		}
	}
	// Engine is not restoring, pick the lastRestored from replica then update LastRestoredBackup
	if !isRestoring {
		for addr, status := range rsMap {
			if lastRestored != "" && status.LastRestored != lastRestored {
				// this error shouldn't prevent the engine from updating the other status
				log.Warnf("Getting different lastRestored values, expecting %v but getting %v even though engine is not restoring",
					lastRestored, status.LastRestored)
				isConsensual = false
			}
			if status.Error != "" {
				log.WithError(errors.New(status.Error)).Warnf("Received restore error from replica %v", addr)
				isConsensual = false
			}
			lastRestored = status.LastRestored
		}
		if isConsensual {
			engine.Status.LastRestoredBackup = lastRestored
		}
	}
	return isRestoring, isConsensual
}

func checkSizeBeforeRestoration(log logrus.FieldLogger, engine *longhorn.Engine, ds *datastore.DataStore) (bool, error) {
	bv, err := ds.GetBackupVolumeRO(engine.Spec.BackupVolume)
	if err != nil {
		return false, err
	}
	// Need to wait for BackupVolume CR syncing with the remote backup target.
	if bv.Status.Size == "" {
		return false, nil
	}
	bvSize, err := strconv.ParseInt(bv.Status.Size, 10, 64)
	if err != nil {
		return false, err
	}

	for i := 0; i < ConflictRetryCount; i++ {
		v, err := ds.GetVolume(engine.Spec.VolumeName)
		if err != nil {
			return false, err
		}

		if bvSize < v.Spec.Size {
			return false, fmt.Errorf("engine monitor: the backup volume size %v is smaller than the size %v of the DR volume %v", bvSize, engine.Spec.VolumeSize, v.Name)
		} else if bvSize > v.Spec.Size {
			// TODO: Find a way to update volume.Spec.Size outside of the controller
			// The volume controller will update `engine.Spec.VolumeSize` later then trigger expansion call
			log.WithField("volume", v.Name).Infof("Preparing to expand the DR volume size from %v to %v", v.Spec.Size, bvSize)
			v.Spec.Size = bvSize
			if _, err := ds.UpdateVolume(v); err != nil {
				if !datastore.ErrorIsConflict(err) {
					return false, err
				}
				log.WithField("volume", v.Name).WithError(err).Warn("Retrying size update for DR volume before restore")
				continue
			}
			return false, nil
		}
	}

	return true, nil
}

func (m *EngineMonitor) restoreBackup(engine *longhorn.Engine, rsMap map[string]*longhorn.RestoreStatus, cliAPIVersion int, engineClientProxy engineapi.EngineClientProxy) error {
	backupVolume, err := m.ds.GetBackupVolumeRO(engine.Spec.BackupVolume)
	if err != nil {
		return errors.Wrapf(err, "failed to get backup volume %v for backup restoration of engine %v", engine.Spec.BackupVolume, engine.Name)
	}

	backupTarget, err := m.ds.GetBackupTargetRO(backupVolume.Spec.BackupTargetName)
	if err != nil {
		return errors.Wrapf(err, "failed to get backup target %s", backupVolume.Spec.BackupTargetName)
	}

	backupTargetClient, err := newBackupTargetClientFromDefaultEngineImage(m.ds, backupTarget)
	if err != nil {
		return errors.Wrapf(err, "cannot get backup target config for backup restoration of engine %v", engine.Name)
	}

	mlog := m.logger.WithFields(logrus.Fields{
		"backupTarget":                backupTargetClient.URL,
		"backupVolume":                engine.Spec.BackupVolume,
		"requestedRestoredBackupName": engine.Spec.RequestedBackupRestore,
		"lastRestoredBackupName":      engine.Status.LastRestoredBackup,
	})

	concurrentLimit, err := m.ds.GetSettingAsInt(types.SettingNameRestoreConcurrentLimit)
	if err != nil {
		return errors.Wrapf(err, "failed to assert %v value", types.SettingNameRestoreConcurrentLimit)
	}

	mlog.Info("Restoring backup")
	lastRestoredBackup := ""
	restoreErrorHandler := handleRestoreError
	if cliAPIVersion < engineapi.CLIVersionFour {
		lastRestoredBackup = engine.Status.LastRestoredBackup
		restoreErrorHandler = handleRestoreErrorForCompatibleEngine
	}
	if err = engineClientProxy.BackupRestore(engine, backupTargetClient.URL, engine.Spec.RequestedBackupRestore, backupVolume.Spec.VolumeName, lastRestoredBackup, backupTargetClient.Credential, int(concurrentLimit)); err != nil {
		if extraErr := restoreErrorHandler(mlog, engine, rsMap, m.restoreBackoff, err); extraErr != nil {
			return extraErr
		}
	}
	if err == nil {
		m.restoreBackoff.DeleteEntry(engine.Name)
	}

	return nil
}

func (m *EngineMonitor) isReachedConcurrentVolumeBackupRestoreLimit() (isUnderLimit bool, err error) {
	limit, err := m.ds.GetSettingAsInt(types.SettingNameConcurrentBackupRestorePerNodeLimit)
	if err != nil {
		return false, err
	}

	return int(m.restoringCounter.GetCount()) >= int(limit), nil
}

func handleRestoreError(log logrus.FieldLogger, engine *longhorn.Engine, rsMap map[string]*longhorn.RestoreStatus, backoff *flowcontrol.Backoff, err error) error {
	taskErr, ok := err.(imclient.TaskError)
	if !ok {
		return errors.Wrapf(err, "failed to restore backup %v in engine monitor, will retry the restore later",
			engine.Spec.RequestedBackupRestore)
	}

	for _, re := range taskErr.ReplicaErrors {
		status, exists := rsMap[re.Address]
		if !exists {
			continue
		}

		if isReplicaRestoreFailedLockError(&re) {
			log.WithError(re).Warnf("Ignored failed locked restore error from replica %v", re.Address)
			// Register the name with a restore backoff entry
			backoff.Next(engine.Name, time.Now())
			continue
		}

		backoff.DeleteEntry(engine.Name)

		if strings.Contains(re.Error(), restoreAlreadyInProgressMsg) ||
			strings.Contains(re.Error(), restoreAlreadyRestoredBackupMsg) {
			log.WithError(re).Warnf("Ignored restore error from replica %v", re.Address)
			continue
		}

		status.Error = re.Error()
	}

	return nil
}

func isReplicaRestoreFailedLockError(err *imclient.ReplicaError) bool {
	failedLock := regexp.MustCompile(restoreGetLockFailedPatternMsg)
	return failedLock.MatchString(err.Error())
}

func handleRestoreErrorForCompatibleEngine(log logrus.FieldLogger, engine *longhorn.Engine, rsMap map[string]*longhorn.RestoreStatus, backoff *flowcontrol.Backoff, err error) error {
	taskErr, ok := err.(imclient.TaskError)
	if !ok {
		return errors.Wrapf(err, "failed to restore backup %v with last restored backup %v in engine monitor",
			engine.Spec.RequestedBackupRestore, engine.Status.LastRestoredBackup)
	}

	for _, re := range taskErr.ReplicaErrors {
		status, exists := rsMap[re.Address]
		if !exists {
			continue
		}

		if isReplicaRestoreFailedLockError(&re) {
			log.WithError(re).Warnf("Ignored failed locked restore error from replica %v", re.Address)
			// Register the name with a restore backoff entry
			backoff.Next(engine.Name, time.Now())
			continue
		}

		backoff.DeleteEntry(engine.Name)
		status.Error = re.Error()
	}
	log.WithError(taskErr).Warnf("Some replicas of the compatible engine failed to start restoring backup %v with last restored backup %v in engine monitor",
		engine.Spec.RequestedBackupRestore, engine.Status.LastRestoredBackup)

	return nil
}

func preCloneCheck(engine *longhorn.Engine) (needClone bool, err error) {
	if engine.Spec.RequestedDataSource == "" {
		return false, nil
	}
	for _, status := range engine.Status.CloneStatus {
		// Already in-cloning or finished cloning the snapshot
		if status != nil && status.State != "" {
			return false, nil
		}
	}
	return true, nil
}

func cloneSnapshot(engine *longhorn.Engine, engineClientProxy engineapi.EngineClientProxy, ds *datastore.DataStore) error {
	sourceVolumeName := types.GetVolumeName(engine.Spec.RequestedDataSource)
	snapshotName := types.GetSnapshotName(engine.Spec.RequestedDataSource)
	sourceEngines, err := ds.ListVolumeEnginesRO(sourceVolumeName)
	if err != nil {
		return err
	}
	if len(sourceEngines) != 1 {
		return fmt.Errorf("failed to get engine for the source volume %v. The source volume has %v engines", sourceVolumeName, len(sourceEngines))
	}
	var sourceEngine *longhorn.Engine
	for _, e := range sourceEngines {
		sourceEngine = e
	}

	fileSyncHTTPClientTimeout, err := ds.GetSettingAsInt(types.SettingNameReplicaFileSyncHTTPClientTimeout)
	if err != nil {
		return err
	}

	grpcTimeoutSeconds, err := ds.GetSettingAsInt(types.SettingNameLongGPRCTimeOut)
	if err != nil {
		return err
	}

	sourceEngineControllerURL := imutil.GetURL(sourceEngine.Status.StorageIP, sourceEngine.Status.Port)
	if err := engineClientProxy.SnapshotClone(engine, snapshotName, sourceEngineControllerURL,
		sourceEngine.Spec.VolumeName, sourceEngine.Name, fileSyncHTTPClientTimeout, grpcTimeoutSeconds); err != nil {
		// There is only 1 replica during volume cloning,
		// so if the cloning failed, it must be that the replica failed to clone.
		for _, status := range engine.Status.CloneStatus {
			status.Error = err.Error()
			status.State = engineapi.ProcessStateError
		}
		return err
	}
	return nil
}

func (ec *EngineController) ReconcileEngineState(e *longhorn.Engine) error {
	if err := ec.removeUnknownReplica(e); err != nil {
		return err
	}

	if err := ec.rebuildNewReplica(e); err != nil {
		return err
	}
	return nil
}

func GetBinaryClientForEngine(e *longhorn.Engine, engines engineapi.EngineClientCollection, image string) (client *engineapi.EngineBinary, err error) {
	defer func() {
		err = errors.Wrapf(err, "cannot get client for engine %v", e.Name)
	}()

	if types.IsDataEngineV2(e.Spec.DataEngine) {
		return nil, nil
	}

	if e.Status.CurrentState != longhorn.InstanceStateRunning {
		return nil, fmt.Errorf("engine is not running")
	}
	if image == "" {
		return nil, fmt.Errorf("require specify engine image")
	}
	if e.Status.IP == "" || e.Status.Port == 0 {
		return nil, fmt.Errorf("require IP and Port")
	}

	client, err = engines.NewEngineClient(&engineapi.EngineClientRequest{
		VolumeName:   e.Spec.VolumeName,
		EngineImage:  image,
		IP:           e.Status.IP,
		Port:         e.Status.Port,
		InstanceName: e.Name,
	})
	if err != nil {
		return nil, err
	}
	return client, nil
}

func (ec *EngineController) removeUnknownReplica(e *longhorn.Engine) error {
	unknownReplicaMap := map[string]longhorn.ReplicaMode{}
	for replica, mode := range e.Status.ReplicaModeMap {
		// unknown replicas have been named as `unknownReplicaPrefix-<replica URL>`
		if strings.HasPrefix(replica, unknownReplicaPrefix) {
			unknownReplicaMap[strings.TrimPrefix(replica, unknownReplicaPrefix)] = mode
		}
	}
	if len(unknownReplicaMap) == 0 {
		return nil
	}

	for url := range unknownReplicaMap {
		engineClientProxy, err := ec.getEngineClientProxy(e, e.Status.CurrentImage)
		if err != nil {
			return errors.Wrapf(err, "failed to get the engine client %v when removing unknown replica %v in mode %v from engine", e.Name, url, unknownReplicaMap[url])
		}

		go func(url string) {
			defer engineClientProxy.Close()

			ec.eventRecorder.Eventf(e, corev1.EventTypeNormal, constant.EventReasonDelete, "Removing unknown replica %v in mode %v from engine", url, unknownReplicaMap[url])
			if err := engineClientProxy.ReplicaRemove(e, url, ""); err != nil {
				ec.eventRecorder.Eventf(e, corev1.EventTypeWarning, constant.EventReasonFailedDeleting, "Failed to remove unknown replica %v in mode %v from engine: %v", url, unknownReplicaMap[url], err)
			} else {
				ec.eventRecorder.Eventf(e, corev1.EventTypeNormal, constant.EventReasonDelete, "Removed unknown replica %v in mode %v from engine", url, unknownReplicaMap[url])
			}
		}(url)
	}
	return nil
}

func (ec *EngineController) rebuildNewReplica(e *longhorn.Engine) error {
	rebuildingInProgress := false
	replicaExists := make(map[string]bool)
	for replica, mode := range e.Status.ReplicaModeMap {
		replicaExists[replica] = true
		if mode == longhorn.ReplicaModeWO {
			rebuildingInProgress = true
			break
		}
	}
	// We cannot rebuild more than one replica at one time
	if rebuildingInProgress {
		ec.logger.WithField("volume", e.Spec.VolumeName).Info("Skipped rebuilding of replica because there is another rebuild in progress")
		return nil
	}
	for replica, addr := range e.Status.CurrentReplicaAddressMap {
		// one is enough
		if !replicaExists[replica] {
			return ec.startRebuilding(e, replica, addr)
		}
	}
	return nil
}

func doesAddressExistInEngine(e *longhorn.Engine, addr string, engineClientProxy engineapi.EngineClientProxy) (bool, error) {
	replicaURLModeMap, err := engineClientProxy.ReplicaList(e)
	if err != nil {
		return false, err
	}

	for url := range replicaURLModeMap {
		// the replica has been rebuilt or in the instance already
		if addr == engineapi.GetAddressFromBackendReplicaURL(url) {
			return true, nil
		}
	}

	return false, nil
}

func (ec *EngineController) startRebuilding(e *longhorn.Engine, replicaName, addr string) (err error) {
	defer func() {
		err = errors.Wrapf(err, "failed to start rebuild for %v of %v", replicaName, e.Name)
	}()

	log := ec.logger.WithFields(logrus.Fields{"volume": e.Spec.VolumeName, "engine": e.Name})

	engineClientProxy, err := ec.getEngineClientProxy(e, e.Status.CurrentImage)
	if err != nil {
		return err
	}
	defer engineClientProxy.Close()

	// we need to know the current status, since ReplicaAddressMap may
	// haven't been updated since last rebuild
	alreadyExists, err := doesAddressExistInEngine(e, addr, engineClientProxy)
	if err != nil {
		return err
	}
	if alreadyExists {
		ec.logger.Infof("Replica %v address %v has been added to the engine already", replicaName, addr)
		return nil
	}

	replicaURL := engineapi.GetBackendReplicaURL(addr)
	go func() {
		autoCleanupSystemGeneratedSnapshot, err := ec.ds.GetSettingAsBool(types.SettingNameAutoCleanupSystemGeneratedSnapshot)
		if err != nil {
			log.WithError(err).Errorf("Failed to get %v setting", types.SettingDefinitionAutoCleanupSystemGeneratedSnapshot)
			return
		}

		fastReplicaRebuild, err := ec.ds.GetSettingAsBool(types.SettingNameFastReplicaRebuildEnabled)
		if err != nil {
			log.WithError(err).Errorf("Failed to get %v setting", types.SettingNameFastReplicaRebuildEnabled)
			return
		}

		fileSyncHTTPClientTimeout, err := ec.ds.GetSettingAsInt(types.SettingNameReplicaFileSyncHTTPClientTimeout)
		if err != nil {
			log.WithError(err).Errorf("Failed to get %v setting", types.SettingNameReplicaFileSyncHTTPClientTimeout)
			return
		}

		grpcTimeoutSeconds, err := ec.ds.GetSettingAsInt(types.SettingNameLongGPRCTimeOut)
		if err != nil {
			log.WithError(err).Errorf("Failed to get %v setting", types.SettingNameLongGPRCTimeOut)
			return
		}

		engineClientProxy, err := ec.getEngineClientProxy(e, e.Status.CurrentImage)
		if err != nil {
			log.WithError(err).Errorf("Failed rebuilding of replica %v", addr)
			ec.eventRecorder.Eventf(e, corev1.EventTypeWarning, constant.EventReasonFailedRebuilding,
				"Failed rebuilding replica with Address %v: %v", addr, err)
			return
		}
		defer engineClientProxy.Close()

		// If enabled, call and wait for SnapshotPurge to clean up system generated snapshot before rebuilding.
		// It is not necessary to check the value of DisableSnapshotPurge here because the webhook prevents enabling
		// AutoCleanupSystemGeneratedSnapshot and DisableSnapshot purge simultaneously.
		if autoCleanupSystemGeneratedSnapshot {
			log.Info("Starting snapshot purge before rebuilding")
			if err := engineClientProxy.SnapshotPurge(e); err != nil {
				log.WithError(err).Error("Failed to start snapshot purge before rebuilding")
				ec.eventRecorder.Eventf(e, corev1.EventTypeWarning, constant.EventReasonFailedStartingSnapshotPurge,
					"Failed to start snapshot purge for engine %v and volume %v before rebuilding: %v", e.Name, e.Spec.VolumeName, err)
				return
			}

			log.Info("Starting snapshot purge before rebuilding, will wait for the purge complete")
			purgeDone := false
			endTime := time.Now().Add(time.Duration(purgeWaitIntervalInSecond) * time.Second)
			ticker := time.NewTicker(2 * EnginePollInterval)
			defer ticker.Stop()
			for !purgeDone && time.Now().Before(endTime) {
				<-ticker.C

				// It may have been a long time since we started purging. Should we proceed?
				e, err := ec.ds.GetEngineRO(e.Name)
				if err != nil {
					log.WithError(err).Error("Failed to get engine and wait for the purge before rebuilding")
					return
				}
				if !shouldProceedToWaitAndRebuild(e, replicaName, addr, log) {
					return
				}

				// Wait for purge complete
				purgeDone = true
				for _, purgeStatus := range e.Status.PurgeStatus {
					if purgeStatus.IsPurging {
						purgeDone = false
						break
					}
				}
			}
			if !purgeDone {
				log.Errorf("Timeout waiting for snapshot purge done before rebuilding, wait interval %v second", purgeWaitIntervalInSecond)
				ec.eventRecorder.Eventf(e, corev1.EventTypeWarning, constant.EventReasonTimeoutSnapshotPurge,
					"Timeout waiting for snapshot purge done before rebuilding volume %v, wait interval %v second",
					e.Spec.VolumeName, purgeWaitIntervalInSecond)
				return
			}
		}

		replica, err := ec.ds.GetReplica(replicaName)
		if err != nil {
			log.WithError(err).Errorf("Failed to get replica %v unable to mark failed rebuild", replica)
			return
		}

		// check and reset replica rebuild failed condition
		replica, err = ec.updateReplicaRebuildFailedCondition(replica, "")
		if err != nil {
			log.WithError(err).Errorf("Failed to update rebuild status information on replica %v", replicaName)
			return
		}

		localSync, err := ec.getFileLocalSync(replica, e)
		if err != nil {
			ec.logger.WithError(err).Warnf("Failed to initiate file local sync for replica %v, use remote sync", replicaName)
		}

		// start rebuild
		if e.Spec.RequestedBackupRestore != "" {
			if e.Spec.NodeID != "" {
				ec.eventRecorder.Eventf(e, corev1.EventTypeNormal, constant.EventReasonRebuilding,
					"Start rebuilding replica %v with Address %v for restore engine %v and volume %v", replicaName, addr, e.Name, e.Spec.VolumeName)
				err = engineClientProxy.ReplicaAdd(e, replicaName, replicaURL, true, fastReplicaRebuild, localSync, fileSyncHTTPClientTimeout, 0)
			}
		} else {
			ec.eventRecorder.Eventf(e, corev1.EventTypeNormal, constant.EventReasonRebuilding,
				"Start rebuilding replica %v with Address %v for normal engine %v and volume %v", replicaName, addr, e.Name, e.Spec.VolumeName)
			err = engineClientProxy.ReplicaAdd(e, replicaName, replicaURL, false, fastReplicaRebuild, localSync, fileSyncHTTPClientTimeout, grpcTimeoutSeconds)
		}

		// For v2 engine, the rebuilding is an async call. We need to wait for the rebuilding start then complete here
		if err == nil && types.IsDataEngineV2(e.Spec.DataEngine) {
			err = ec.waitForV2EngineRebuild(e, replicaName, grpcTimeoutSeconds)
		}

		if err != nil {
			replicaRebuildErrMsg := err.Error()

			log.WithError(err).Errorf("Failed to rebuild replica %v", addr)
			ec.eventRecorder.Eventf(e, corev1.EventTypeWarning, constant.EventReasonFailedRebuilding, "Failed rebuilding replica with Address %v: %v", addr, err)
			// we've sent out event to notify user. we don't want to
			// automatically handle it because it may cause chain
			// reaction to create numerous new replicas if we set
			// the replica to failed.
			// user can decide to delete it then we will try again
			log.Infof("Removing failed rebuilding replica %v", addr)
			if err := engineClientProxy.ReplicaRemove(e, replicaURL, replicaName); err != nil {
				log.WithError(err).Warnf("Failed to remove rebuilding replica %v", addr)
				ec.eventRecorder.Eventf(e, corev1.EventTypeWarning, constant.EventReasonFailedDeleting,
					"Failed to remove rebuilding replica %v with address %v for engine %v and volume %v due to rebuilding failure: %v",
					replicaName, addr, e.Name, e.Spec.VolumeName, err)
			}

			// Before we mark the Replica as Failed automatically, we want to check the Backoff to avoid recreating new
			// Replicas too quickly. If the Replica is still in the Backoff period, we will leave the Replica alone. If
			// it is past the Backoff period, we'll try to mark the Replica as Failed and increase the Backoff period
			// for the next failure.
			if !ec.backoff.IsInBackOffSinceUpdate(e.Name, time.Now()) {
				replica, err = ec.updateReplicaRebuildFailedCondition(replica, replicaRebuildErrMsg)
				if err != nil {
					log.WithError(err).Errorf("Failed to update rebuild status information on replica %v", replicaName)
					return
				}

				setReplicaFailedAt(replica, util.Now())
				replica.Spec.DesireState = longhorn.InstanceStateStopped
				if _, err := ec.ds.UpdateReplica(replica); err != nil {
					log.WithError(err).Errorf("Unable to mark failed rebuild on replica %v", replicaName)
					return
				}
				// Now that the Replica can actually be recreated, we can move up the Backoff.
				ec.backoff.Next(e.Name, time.Now())
				backoffTime := ec.backoff.Get(e.Name).Seconds()
				log.Infof("Marked failed rebuild on replica %v, backoff period is now %v seconds", replicaName, backoffTime)
				return
			}
			log.Debugf("Engine is still in backoff for replica %v rebuild failure", replicaName)
			return
		}
		// Replica rebuild succeeded, clear Backoff.
		ec.backoff.DeleteEntry(e.Name)
		ec.eventRecorder.Eventf(e, corev1.EventTypeNormal, constant.EventReasonRebuilt,
			"Replica %v with Address %v has been rebuilt for volume %v", replicaName, addr, e.Spec.VolumeName)

		// If enabled, call SnapshotPurge to clean up system generated snapshot after rebuilding.
		// It is not necessary to check the value of DisableSnapshotPurge here because the webhook prevents enabling
		// AutoCleanupSystemGeneratedSnapshot and DisableSnapshot purge simultaneously.
		if autoCleanupSystemGeneratedSnapshot {
			log.Info("Starting snapshot purge after rebuilding")
			if err := engineClientProxy.SnapshotPurge(e); err != nil {
				log.WithError(err).Error("Failed to start snapshot purge after rebuilding")
				ec.eventRecorder.Eventf(e, corev1.EventTypeWarning, constant.EventReasonFailedStartingSnapshotPurge,
					"Failed to start snapshot purge for engine %v and volume %v after rebuilding: %v", e.Name, e.Spec.VolumeName, err)
				return
			}
		}
	}()

	// Wait until engine confirmed that rebuild started
	if err := wait.PollUntilContextTimeout(context.Background(), EnginePollInterval, EnginePollTimeout, true, func(context.Context) (bool, error) {
		return doesAddressExistInEngine(e, addr, engineClientProxy)
	}); err != nil {
		return err
	}
	return nil
}

// getFileLocalSync retrieves details for local file sync between the target replica
// and another eligible replica on the same node. It returns an object with the source
// and target paths for the local sync, or nil if no other eligible replica is found.
// Any error encountered during the process is returned.
func (ec *EngineController) getFileLocalSync(targetReplica *longhorn.Replica, engine *longhorn.Engine) (*etypes.FileLocalSync, error) {
	// Retrieve a map of replicas grouped by node for the engine's volume
	replicaMapByNode, err := ec.ds.ListVolumeReplicasROMapByNode(engine.Spec.VolumeName)
	if err != nil {
		return nil, err
	}

	// Check if there are multiple replicas on the target replica's node,
	// do nothing if there is no other available replica to sync locally from.
	if len(replicaMapByNode[targetReplica.Spec.NodeID]) <= 1 {
		return nil, nil
	}

	var localSync *etypes.FileLocalSync
	for _, nodeReplica := range replicaMapByNode[targetReplica.Spec.NodeID] {
		// Skip the target replica itself
		if nodeReplica.Name == targetReplica.Name {
			continue
		}

		// Skip replicas marked for deletion
		if nodeReplica.DeletionTimestamp != nil {
			continue
		}

		// Skip replicas that are not in the running state
		if nodeReplica.Status.CurrentState != longhorn.InstanceStateRunning {
			continue
		}

		// Set the local sync details
		localSync = &etypes.FileLocalSync{
			SourcePath: filepath.Join(nodeReplica.Spec.DiskPath, "replicas", nodeReplica.Spec.DataDirectoryName),
			TargetPath: filepath.Join(targetReplica.Spec.DiskPath, "replicas", targetReplica.Spec.DataDirectoryName),
		}
		break
	}

	if localSync == nil {
		return nil, errors.New("no other eligible replica found for local sync")
	}

	return localSync, nil
}

// updateReplicaRebuildFailedCondition updates the rebuild failed condition if replica rebuilding failed
func (ec *EngineController) updateReplicaRebuildFailedCondition(replica *longhorn.Replica, errMsg string) (*longhorn.Replica, error) {
	replicaRebuildFailedReason, conditionStatus, err := ec.getReplicaRebuildFailedReason(replica.Spec.NodeID, errMsg)
	if err != nil {
		return nil, err
	}

	replica.Status.Conditions = types.SetCondition(
		replica.Status.Conditions,
		longhorn.ReplicaConditionTypeRebuildFailed,
		conditionStatus,
		replicaRebuildFailedReason,
		errMsg)

	replica, err = ec.ds.UpdateReplicaStatus(replica)

	return replica, err
}

func (ec *EngineController) getReplicaRebuildFailedReason(replicaNodeID, errMsg string) (failedReason string, conditionStatus longhorn.ConditionStatus, err error) {
	failedReason, conditionStatus, isRebuildingFailedByNetwork := getReplicaRebuildFailedReasonFromError(errMsg)
	if isRebuildingFailedByNetwork {
		replicaNode, err := ec.ds.GetNodeRO(replicaNodeID)
		if err != nil {
			return "", "", err
		}

		replicaRebuildFailedCondition := types.GetCondition(replicaNode.Status.Conditions, longhorn.NodeConditionTypeReady)
		switch replicaRebuildFailedCondition.Reason {
		case longhorn.NodeConditionReasonManagerPodDown, longhorn.NodeConditionReasonKubernetesNodeGone, longhorn.NodeConditionReasonKubernetesNodeNotReady:
			failedReason = replicaRebuildFailedCondition.Reason
		}
	}

	return failedReason, conditionStatus, nil
}

func getReplicaRebuildFailedReasonFromError(errMsg string) (string, longhorn.ConditionStatus, bool) {
	switch {
	case strings.Contains(errMsg, longhorn.ReplicaRebuildFailedCanceledErrorMSG):
		fallthrough
	case strings.Contains(errMsg, longhorn.ReplicaRebuildFailedDeadlineExceededErrorMSG):
		fallthrough
	case strings.Contains(errMsg, longhorn.ReplicaRebuildFailedUnavailableErrorMSG):
		return longhorn.ReplicaConditionReasonRebuildFailedDisconnection, longhorn.ConditionStatusTrue, true
	case errMsg == "":
		return "", longhorn.ConditionStatusFalse, false
	default:
		return longhorn.ReplicaConditionReasonRebuildFailedGeneral, longhorn.ConditionStatusTrue, false
	}
}

func (ec *EngineController) waitForV2EngineRebuild(e *longhorn.Engine, replicaName string, timeout int64) (err error) {
	if !types.IsDataEngineV2(e.Spec.DataEngine) {
		return nil
	}

	ticker := time.NewTicker(EnginePollInterval)
	defer ticker.Stop()
	timer := time.NewTimer(time.Duration(timeout) * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ticker.C:
			e, err = ec.ds.GetEngineRO(e.Name)
			if err != nil {
				// There is no need to continue if the engine is not found
				if apierrors.IsNotFound(err) {
					return errors.Wrapf(err, "engine %v not found during v2 replica %s rebuild wait", e.Name, replicaName)
				}
				// There may be something wrong with the indexer or the API sever, will retry
				continue
			}
			if e.Spec.ReplicaAddressMap[replicaName] == "" {
				return fmt.Errorf("unknown replica %v for engine", replicaName)
			}
			// There is no need to continue when the replica is not found or the replica is not in a valid state for rebuilding
			r, err := ec.ds.GetReplicaRO(replicaName)
			if err != nil {
				return err
			}
			if r.Status.CurrentState != longhorn.InstanceStateRunning {
				return fmt.Errorf("replica %v is state %s, which is invalid for rebuilding", replicaName, r.Status.CurrentState)
			}
			if e.Status.ReplicaModeMap[replicaName] == longhorn.ReplicaModeRW {
				return nil
			}
			if e.Status.ReplicaModeMap[replicaName] == longhorn.ReplicaModeERR {
				return fmt.Errorf("replica %v is in ERR mode, which is invalid for rebuilding", replicaName)
			}
			if e.Status.ReplicaModeMap[replicaName] == "" {
				continue
			}
			// For a rebuilding replica (with mode WO), there should be a corresponding rebuilding status
			rebuildingStatus := e.Status.RebuildStatus[engineapi.GetBackendReplicaURL(e.Status.CurrentReplicaAddressMap[replicaName])]
			if rebuildingStatus == nil {
				continue
			}
			if rebuildingStatus.State == engineapi.ProcessStateError || rebuildingStatus.Error != "" {
				return fmt.Errorf("failed to wait for v2 replica %s rebuild, rebuilding state %s, error: %v", replicaName, rebuildingStatus.State, rebuildingStatus.Error)
			}
			if rebuildingStatus.State == engineapi.ProcessStateComplete {
				return nil
			}
		case <-timer.C:
			return fmt.Errorf("timeout waiting for replica %v to be rebuilt", replicaName)
		}
	}
}

func (ec *EngineController) Upgrade(e *longhorn.Engine, log *logrus.Entry) (err error) {
	defer func() {
		err = errors.Wrapf(err, "failed to live upgrade image for %v", e.Name)
	}()

	if types.IsDataEngineV1(e.Spec.DataEngine) {
		engineClientProxy, err := ec.getEngineClientProxy(e, e.Spec.Image)
		if err != nil {
			return err
		}
		defer engineClientProxy.Close()

		version, err := engineClientProxy.VersionGet(e, false)
		if err != nil {
			return err
		}

		// Don't use image with different image name but same commit here. It
		// will cause live replica to be removed. Volume controller should filter those.
		if version.ClientVersion.GitCommit != version.ServerVersion.GitCommit {
			log.Infof("Upgrading engine from %v to %v", e.Status.CurrentImage, e.Spec.Image)
			if err := ec.UpgradeEngineInstance(e, log); err != nil {
				return err
			}
		}
	} else {
		return errors.Wrapf(err, "upgrading engine %v with data engine %v is not supported", e.Name, e.Spec.DataEngine)
	}

	log.Infof("Engine has been upgraded from %v to %v", e.Status.CurrentImage, e.Spec.Image)
	e.Status.CurrentImage = e.Spec.Image
	e.Status.CurrentReplicaAddressMap = e.Spec.UpgradedReplicaAddressMap
	// reset ReplicaModeMap to reflect the new replicas
	e.Status.ReplicaModeMap = nil
	e.Status.ReplicaTransitionTimeMap = nil
	e.Status.RestoreStatus = nil
	e.Status.RebuildStatus = nil
	return nil
}

func (ec *EngineController) UpgradeEngineInstance(e *longhorn.Engine, log *logrus.Entry) error {
	frontend := e.Spec.Frontend
	if e.Spec.DisableFrontend {
		frontend = longhorn.VolumeFrontendEmpty
	}

	im, err := ec.ds.GetInstanceManagerRO(e.Status.InstanceManagerName)
	if err != nil {
		return err
	}

	c, err := engineapi.NewInstanceManagerClient(im, false)
	if err != nil {
		return err
	}
	defer func(c io.Closer) {
		if closeErr := c.Close(); closeErr != nil {
			ec.logger.WithError(closeErr).Warn("Failed to close instance manager client")
		}
	}(c)

	engineReplicaTimeout, err := ec.ds.GetSettingAsInt(types.SettingNameEngineReplicaTimeout)
	if err != nil {
		return err
	}

	fileSyncHTTPClientTimeout, err := ec.ds.GetSettingAsInt(types.SettingNameReplicaFileSyncHTTPClientTimeout)
	if err != nil {
		return err
	}

	v, err := ec.ds.GetVolumeRO(e.Spec.VolumeName)
	if err != nil {
		return err
	}

	cliAPIVersion, err := ec.ds.GetDataEngineImageCLIAPIVersion(e.Spec.Image, e.Spec.DataEngine)
	if err != nil {
		return err
	}

	processBinary, err := c.InstanceGetBinary(e.Spec.DataEngine, e.Name, string(longhorn.InstanceManagerTypeEngine), "")
	if err != nil {
		return errors.Wrapf(err, "failed to get the binary of the current engine instance")
	}
	if strings.Contains(processBinary, types.GetImageCanonicalName(e.Spec.Image)) {
		log.Infof("The existing engine instance already has the new engine image %v", e.Spec.Image)
		return nil
	}

	engineInstance, err := c.EngineInstanceUpgrade(&engineapi.EngineInstanceUpgradeRequest{
		Engine:                           e,
		VolumeFrontend:                   frontend,
		EngineReplicaTimeout:             engineReplicaTimeout,
		ReplicaFileSyncHTTPClientTimeout: fileSyncHTTPClientTimeout,
		DataLocality:                     v.Spec.DataLocality,
		EngineCLIAPIVersion:              cliAPIVersion,
	})
	if err != nil {
		return err
	}

	e.Status.Port = int(engineInstance.Status.PortStart)
	e.Status.UUID = engineInstance.Status.UUID
	return nil
}

// isResponsibleFor picks a running node that has e.Status.CurrentImage deployed.
// We need e.Status.CurrentImage deployed on the node to make request to the corresponding engine instance.
// Prefer picking the node e.Spec.NodeID if it meet the above requirement.
func (ec *EngineController) isResponsibleFor(e *longhorn.Engine, defaultEngineImage string) (bool, error) {
	var err error
	defer func() {
		err = errors.Wrap(err, "error while checking isResponsibleFor")
	}()

	// If a regular RWX is delinquent, try to switch ownership quickly to the owner node of the share manager CR
	isOwnerNodeDelinquent, err := ec.ds.IsNodeDelinquent(e.Status.OwnerID, e.Spec.VolumeName)
	if err != nil {
		return false, err
	}
	isSpecNodeDelinquent, err := ec.ds.IsNodeDelinquent(e.Spec.NodeID, e.Spec.VolumeName)
	if err != nil {
		return false, err
	}
	preferredOwnerID := e.Spec.NodeID
	if isOwnerNodeDelinquent || isSpecNodeDelinquent {
		sm, err := ec.ds.GetShareManager(e.Spec.VolumeName)
		if err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		if sm != nil {
			preferredOwnerID = sm.Status.OwnerID
		}
	}

	isResponsible := isControllerResponsibleFor(ec.controllerID, ec.ds, e.Name, preferredOwnerID, e.Status.OwnerID)

	// The engine is not running, the owner node doesn't need to have e.Status.CurrentImage
	// Fall back to the default logic where we pick a running node to be the owner
	if e.Status.CurrentImage == "" {
		return isResponsible, nil
	}

	if types.IsDataEngineV1(e.Spec.DataEngine) {
		readyNodesWithEI, err := ec.ds.ListReadyNodesContainingEngineImageRO(e.Status.CurrentImage)
		if err != nil {
			return false, errors.Wrapf(err, "failed to list ready nodes containing engine image %v", e.Status.CurrentImage)
		}
		// No node in the system has the e.Status.CurrentImage,
		// Fall back to the default logic where we pick a running node to be the owner
		if len(readyNodesWithEI) == 0 {
			return isResponsible, nil
		}
	}

	preferredOwnerDataEngineAvailable, err := ec.ds.CheckDataEngineImageReadiness(e.Status.CurrentImage, e.Spec.DataEngine, e.Spec.NodeID)
	if err != nil {
		return false, err
	}
	currentOwnerDataEngineAvailable, err := ec.ds.CheckDataEngineImageReadiness(e.Status.CurrentImage, e.Spec.DataEngine, e.Status.OwnerID)
	if err != nil {
		return false, err
	}
	currentNodeDataEngineAvailable, err := ec.ds.CheckDataEngineImageReadiness(e.Status.CurrentImage, e.Spec.DataEngine, ec.controllerID)
	if err != nil {
		return false, err
	}

	isPreferredOwner := currentNodeDataEngineAvailable && isResponsible
	continueToBeOwner := currentNodeDataEngineAvailable && !preferredOwnerDataEngineAvailable && ec.controllerID == e.Status.OwnerID
	requiresNewOwner := currentNodeDataEngineAvailable && !preferredOwnerDataEngineAvailable && !currentOwnerDataEngineAvailable

	return isPreferredOwner || continueToBeOwner || requiresNewOwner, nil
}

func removeInvalidEngineOpStatus(e *longhorn.Engine) {
	tcpReplicaAddrMap := map[string]struct{}{}
	for _, addr := range e.Status.CurrentReplicaAddressMap {
		tcpReplicaAddrMap[engineapi.GetBackendReplicaURL(addr)] = struct{}{}
	}
	for tcpAddr := range e.Status.PurgeStatus {
		if _, exists := tcpReplicaAddrMap[tcpAddr]; !exists {
			delete(e.Status.PurgeStatus, tcpAddr)
		}
	}
	for tcpAddr := range e.Status.RestoreStatus {
		if _, exists := tcpReplicaAddrMap[tcpAddr]; !exists {
			delete(e.Status.RestoreStatus, tcpAddr)
		}
	}
	for tcpAddr := range e.Status.RebuildStatus {
		if _, exists := tcpReplicaAddrMap[tcpAddr]; !exists {
			delete(e.Status.RebuildStatus, tcpAddr)
		}
	}
	for tcpAddr := range e.Status.CloneStatus {
		if _, exists := tcpReplicaAddrMap[tcpAddr]; !exists {
			delete(e.Status.CloneStatus, tcpAddr)
		}
	}
}

// shouldProceedToRebuild checks a variety of conditions that may cause us not to proceed with waiting for snapshot
// purge and/or rebuilding a replica. We pass the logger to it so it can decide what level to log at depending on the
// issue. We do not return any errors because shouldProceedToRebuild is called by startRebuilding in a goroutine.
func shouldProceedToWaitAndRebuild(e *longhorn.Engine, replicaName, originalReplicaAddr string, log *logrus.Entry) bool {
	// The engine is no longer running.
	if e.Status.CurrentState != longhorn.InstanceStateRunning {
		log.Errorf("Failed to proceed to rebuild since engine is state %v", e.Status.CurrentState)
		return false
	}

	// The volume controller no longer expects the engine to be running.
	if e.Spec.DesireState != longhorn.InstanceStateRunning {
		log.Warnf("Failed to proceed to rebuild since engine state should be %v",
			e.Spec.DesireState)
		return false
	}

	// The volume controller no longer expects the engine to communicate with a replica at this address.
	updatedAddr, ok := e.Spec.ReplicaAddressMap[replicaName]
	if !ok {
		log.Warnf("Failed to proceed to rebuild since replica %v is no longer in the replicaAddressMap", replicaName)
		return false
	}
	if originalReplicaAddr != updatedAddr {
		log.Warnf("Failed to proceed to rebuild since the address for replica %v has been updated from %v to %v",
			replicaName, originalReplicaAddr, updatedAddr)
		return false
	}

	return true
}

// needStatusUpdate checks whether we should update the engine status and whether that update should be rate limited. We
// return:
// - false, false if no fields change
// - true, false if any field besides the size of the volume-head snapshot changes
// - true, false if the change in size of the volume-head snapshot exceeds a threshold
// - true, true if the change in size of the volume-head does not exceed a threshold
func (m *EngineMonitor) needStatusUpdate(existing, new *longhorn.Engine) (needStatusUpdate, rateLimited bool) {
	if needStatusUpdateBesidesSize(&existing.Status, &new.Status) {
		return true, false
	}

	existingSnapshot, existingSnapshotOK := existing.Status.Snapshots[etypes.VolumeHeadName]
	newSnapshot, newSnapshotOK := new.Status.Snapshots[etypes.VolumeHeadName]
	if !existingSnapshotOK || !newSnapshotOK {
		return false, false // We can't do anything that needStatusUpdateBesidesSize hasn't already.
	}

	// Now, compare only the volume-head sizes.
	var existingSizeInt, newSizeInt int64
	var err error
	if existingSizeInt, err = strconv.ParseInt(existingSnapshot.Size, 10, 64); err != nil {
		m.logger.WithError(err).Warnf("Failed to parse %s size %v", etypes.VolumeHeadName, existingSnapshot.Size)
		return false, false
	}
	if newSizeInt, err = strconv.ParseInt(newSnapshot.Size, 10, 64); err != nil {
		m.logger.WithError(err).Warnf("Failed to parse %s size %v", etypes.VolumeHeadName, newSnapshot.Size)
		return false, false
	}
	return needSizeUpdate(existingSizeInt, newSizeInt, new.Spec.VolumeSize)
}

// needStatusUpdateBesidesSize does half of the work of needStatusUpdate. It is broken out because only one caller
// should consider the size of the volume-head snapshot when deciding whether to update. All other callers should not
// consider a change in volume-head snapshot size a reason to update. If they do, they will circumvent rate limiting.
func needStatusUpdateBesidesSize(existing, new *longhorn.EngineStatus) bool {
	// If we don't have a volume-head snapshot in new and old status, just compare statuses directly.
	existingSnapshot, existingSnapshotOK := existing.Snapshots[etypes.VolumeHeadName]
	newSnapshot, newSnapshotOK := new.Snapshots[etypes.VolumeHeadName]
	if !existingSnapshotOK || !newSnapshotOK {
		return !reflect.DeepEqual(existing, new)
	}

	// Otherwise, compare without the size of volume-head.
	existingSize := existingSnapshot.Size
	existingSnapshot.Size = newSnapshot.Size
	needUpdate := !reflect.DeepEqual(existing, new)
	existingSnapshot.Size = existingSize
	return needUpdate
}

// needSizeUpdate returns needSizeUpdate == true if the caller should attempt to update the engine status based on size
// alone. In addition, it returns rateLimited == true if the change in size does not exceed a threshold.
func needSizeUpdate(existingSize, newSize, nominalSize int64) (needSizeUpdate, rateLimited bool) {
	if newSize == existingSize {
		return false, false
	}
	if newSize-existingSize >= sizeThreshold(nominalSize) {
		// We don't need a reservation to update sizes exceeding the threshold.
		return true, false
	}
	return true, true
}

// sizeThreshold calculates the difference in size for which we will update status (regardless of rate limiting).
func sizeThreshold(nominalSize int64) int64 {
	if nominalSize <= 0 {
		return 0
	}
	if nominalSize <= 100*util.GiB {
		return nominalSize / 1024 // Update status for change > 1/1024 nominal size.
	}
	return 100 * util.MiB // Update status for any change > 100 MiB.
}

func shouldSkipEngineDeletion(imState longhorn.InstanceManagerState, isRWXVolume bool) (canSkip bool, reason string) {
	// For a RWX volume, the node down, for example, caused by kubelet restart, leads to share-manager pod
	// deletion/recreation and volume detachment/attachment. Then, the newly created share-manager pod blindly mounts
	// the longhorn volume inside /dev/longhorn/<pvc-name> and exports it. To avoid mounting a dead and orphaned volume,
	// try to clean up the engine instance as well as the orphaned iscsi device regardless of the instance-manager
	// status.
	if isRWXVolume {
		return false, ""
	}

	// If the instance manager is in an unknown state, we should at least attempt instance deletion.
	if imState == longhorn.InstanceManagerStateRunning || imState == longhorn.InstanceManagerStateUnknown {
		return false, ""
	}

	return true, fmt.Sprintf("instance manager is in %v state", imState)
}

func canIgnoreEngineDeletionFailure(imState longhorn.InstanceManagerState, isRWXVolume, isDelinquent bool) (canIgnore bool, reason string) {
	// Instance deletion is always best effort for an unknown instance manager.
	if imState == longhorn.InstanceManagerStateUnknown {
		return true, fmt.Sprintf("instance manager is in %v state", imState)
	}

	// The remaining reasons apply only to RWX volumes.
	if !isRWXVolume {
		return false, ""
	}

	// Try the best to delete engine instance.
	// To prevent that the volume is stuck at detaching state, ignore the error when volume is a RWX volume and the
	// instance manager is not running or the RWX volume is currently delinquent.
	//
	// If the engine instance of a RWX volume is not deleted successfully: If a RWX volume is on node A and the network
	// of this node is partitioned, the owner of the share manager (SM) is transferred to node B. The engine instance
	// and the block device (/dev/longhorn/pvc-xxx) on the node A become orphaned. If the network of the node A gets
	// back to normal, the SM can be shifted back to node A. After shifting to node A, the first reattachment fail due
	// to the IO error resulting from the orphaned engine instance and block device. Then, the detachment will trigger
	// the teardown of the problematic engine instance and block device. The next reattachment then will succeed.
	if imState != longhorn.InstanceManagerStateRunning {
		return true, fmt.Sprintf("instance manager is in %v state for the RWX volume", imState)
	}

	if isDelinquent {
		return true, "the RWX volume is delinquent"
	}

	return false, ""
}
