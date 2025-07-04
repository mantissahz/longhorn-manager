package controller

import (
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/kubernetes/pkg/controller"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientset "k8s.io/client-go/kubernetes"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/longhorn/longhorn-manager/constant"
	"github.com/longhorn/longhorn-manager/datastore"
	"github.com/longhorn/longhorn-manager/engineapi"
	"github.com/longhorn/longhorn-manager/types"
	"github.com/longhorn/longhorn-manager/util"

	longhorn "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta2"
)

const (
	snapshotErrorLost = "lost track of the corresponding snapshot info inside volume engine"
)

type SnapshotController struct {
	*baseController

	// which namespace controller is running with
	namespace string
	// use as the OwnerID of the controller
	controllerID string

	kubeClient    clientset.Interface
	eventRecorder record.EventRecorder

	ds                     *datastore.DataStore
	cacheSyncs             []cache.InformerSynced
	engineClientCollection engineapi.EngineClientCollection

	proxyConnCounter util.Counter
}

func NewSnapshotController(
	logger logrus.FieldLogger,
	ds *datastore.DataStore,
	scheme *runtime.Scheme,
	kubeClient clientset.Interface,
	namespace string,
	controllerID string,
	engineClientCollection engineapi.EngineClientCollection,
	proxyConnCounter util.Counter,
) (*SnapshotController, error) {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(logrus.Infof)
	// TODO: remove the wrapper when every clients have moved to use the clientset.
	eventBroadcaster.StartRecordingToSink(&v1core.EventSinkImpl{
		Interface: v1core.New(kubeClient.CoreV1().RESTClient()).Events(""),
	})

	sc := &SnapshotController{
		baseController: newBaseController("longhorn-snapshot", logger),

		namespace:              namespace,
		controllerID:           controllerID,
		kubeClient:             kubeClient,
		eventRecorder:          eventBroadcaster.NewRecorder(scheme, corev1.EventSource{Component: "longhorn-snapshot-controller"}),
		ds:                     ds,
		engineClientCollection: engineClientCollection,
		proxyConnCounter:       proxyConnCounter,
	}

	var err error
	if _, err = ds.SnapshotInformer.AddEventHandlerWithResyncPeriod(cache.ResourceEventHandlerFuncs{
		AddFunc:    sc.enqueueSnapshot,
		UpdateFunc: func(old, cur interface{}) { sc.enqueueSnapshot(cur) },
		DeleteFunc: sc.enqueueSnapshot,
	}, 0); err != nil {
		return nil, err
	}
	sc.cacheSyncs = append(sc.cacheSyncs, ds.SnapshotInformer.HasSynced)

	if _, err = ds.EngineInformer.AddEventHandlerWithResyncPeriod(cache.ResourceEventHandlerFuncs{
		UpdateFunc: sc.enqueueEngineChange,
	}, 0); err != nil {
		return nil, err
	}
	sc.cacheSyncs = append(sc.cacheSyncs, ds.EngineInformer.HasSynced)

	if _, err = ds.VolumeInformer.AddEventHandlerWithResyncPeriod(cache.ResourceEventHandlerFuncs{
		UpdateFunc: sc.enqueueVolumeChange,
		DeleteFunc: sc.enqueueVolumeDeleted,
	}, 0); err != nil {
		return nil, err
	}
	sc.cacheSyncs = append(sc.cacheSyncs, ds.VolumeInformer.HasSynced)

	if _, err = ds.SettingInformer.AddEventHandlerWithResyncPeriod(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(old, cur interface{}) { sc.enqueueSettingChange(cur) },
	}, 0); err != nil {
		return nil, err
	}
	sc.cacheSyncs = append(sc.cacheSyncs, ds.SettingInformer.HasSynced)

	return sc, nil
}

func (sc *SnapshotController) enqueueSnapshot(obj interface{}) {
	key, err := controller.KeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("failed to get key for object %#v: %v", obj, err))
		return
	}

	sc.queue.Add(key)
}

func (sc *SnapshotController) enqueueEngineChange(oldObj, curObj interface{}) {
	curEngine, ok := curObj.(*longhorn.Engine)
	if !ok {
		deletedState, ok := curObj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("received unexpected obj: %#v", curObj))
			return
		}
		// use the last known state, to enqueue, dependent objects
		curEngine, ok = deletedState.Obj.(*longhorn.Engine)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("DeletedFinalStateUnknown contained invalid object: %#v", deletedState.Obj))
			return
		}
	}

	vol, err := sc.ds.GetVolumeRO(curEngine.Spec.VolumeName)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("snapshot controller failed to get volume %v when enqueuing engine %v: %v", curEngine.Spec.VolumeName, curEngine.Name, err))
		return
	}

	if vol.Status.OwnerID != sc.controllerID {
		return
	}

	oldEngine, ok := oldObj.(*longhorn.Engine)
	if !ok {
		deletedState, ok := oldObj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("received unexpected obj: %#v", oldObj))
			return
		}
		// use the last known state, to enqueue, dependent objects
		oldEngine, ok = deletedState.Obj.(*longhorn.Engine)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("DeletedFinalStateUnknown contained invalid object: %#v", deletedState.Obj))
			return
		}
	}

	needEnqueueSnapshots := curEngine.Status.CurrentState != oldEngine.Status.CurrentState ||
		!reflect.DeepEqual(curEngine.Status.PurgeStatus, oldEngine.Status.PurgeStatus) ||
		!reflect.DeepEqual(curEngine.Status.Snapshots, oldEngine.Status.Snapshots)

	if !needEnqueueSnapshots {
		return
	}

	snapshots, err := sc.ds.ListVolumeSnapshotsRO(curEngine.Spec.VolumeName)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("failed to list snapshots for volume %v when enqueuing engine %v: %v", curEngine.Spec.VolumeName, oldEngine.Name, err))
		return
	}

	snapshots = filterSnapshotsForEngineEnqueuing(oldEngine, curEngine, snapshots)
	for _, snap := range snapshots {
		sc.enqueueSnapshot(snap)
	}
}

func filterSnapshotsForEngineEnqueuing(oldEngine, curEngine *longhorn.Engine, snapshots map[string]*longhorn.Snapshot) map[string]*longhorn.Snapshot {
	targetSnapshots := make(map[string]*longhorn.Snapshot)

	if curEngine.Status.CurrentState != oldEngine.Status.CurrentState {
		return snapshots
	}

	if !reflect.DeepEqual(curEngine.Status.PurgeStatus, oldEngine.Status.PurgeStatus) {
		for snapName, snap := range snapshots {
			if snap.DeletionTimestamp != nil {
				targetSnapshots[snapName] = snap
			}
		}
	}

	for snapName, snap := range snapshots {
		if !reflect.DeepEqual(oldEngine.Status.Snapshots[snapName], curEngine.Status.Snapshots[snapName]) {
			targetSnapshots[snapName] = snap
		}
	}

	return targetSnapshots
}

// There is a race condition for that all snapshot controllers will not process some snapshots when the volume owner ID changes.
// https://github.com/longhorn/longhorn/issues/10874#issuecomment-2870915401
// In that case, snapshot controllers should enqueue the snapshots again when the volume owner ID changes.
func (sc *SnapshotController) enqueueVolumeChange(old, new interface{}) {
	oldVol, ok := old.(*longhorn.Volume)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("received unexpected obj: %#v", old))
		return
	}
	newVol, ok := new.(*longhorn.Volume)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("received unexpected obj: %#v", new))
		return
	}
	if !newVol.DeletionTimestamp.IsZero() {
		return
	}
	if oldVol.Status.OwnerID == newVol.Status.OwnerID {
		return
	}

	va, err := sc.ds.GetLHVolumeAttachmentByVolumeName(newVol.Name)
	if err != nil {
		sc.logger.WithError(err).Warnf("Failed to get volume attachment for volume %s", newVol.Name)
		return
	}
	if va == nil || va.Spec.AttachmentTickets == nil {
		return
	}

	snapshots, err := sc.ds.ListVolumeSnapshotsRO(newVol.Name)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("snapshot controller failed to list snapshots when enqueuing volume %v: %v", newVol.Name, err))
		return
	}
	for _, snap := range snapshots {
		// Longhorn#10874:
		// Requeue the snapshot if there is an attachment ticket for it to ensure the volumeattachment can be cleaned up after the snapshot is created.
		attachmentTicketID := longhorn.GetAttachmentTicketID(longhorn.AttacherTypeSnapshotController, snap.Name)
		if va.Spec.AttachmentTickets[attachmentTicketID] != nil {
			sc.enqueueSnapshot(snap)
		}
	}
}

func (sc *SnapshotController) enqueueVolumeDeleted(obj interface{}) {
	vol, ok := obj.(*longhorn.Volume)
	if !ok {
		deletedState, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("received unexpected obj: %#v", obj))
			return
		}
		// use the last known state, to enqueue, dependent objects
		vol, ok = deletedState.Obj.(*longhorn.Volume)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("DeletedFinalStateUnknown contained invalid object: %#v", deletedState.Obj))
			return
		}
	}
	if vol.DeletionTimestamp.IsZero() {
		return
	}

	snapshots, err := sc.ds.ListVolumeSnapshotsRO(vol.Name)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("snapshot controller failed to list snapshots when enqueuing volume %v: %v", vol.Name, err))
		return
	}
	for _, snap := range snapshots {
		sc.enqueueSnapshot(snap)
	}
}

// If DisableSnapshotPurge is transitioning from true to false, there may be a backlog of snapshots with
// deletionTimestamps that we are ignoring. Requeue all such snapshots.
func (sc *SnapshotController) enqueueSettingChange(obj interface{}) {
	setting, ok := obj.(*longhorn.Setting)
	if !ok {
		deletedState, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("received unexpected obj: %#v", obj))
			return
		}

		// use the last known state, to enqueue, dependent objects
		setting, ok = deletedState.Obj.(*longhorn.Setting)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("DeletedFinalStateUnknown contained invalid object: %#v", deletedState.Obj))
			return
		}
	}

	if types.SettingName(setting.Name) != types.SettingNameDisableSnapshotPurge || setting.Value == "true" {
		return
	}

	snapshots, err := sc.ds.ListSnapshotsRO(labels.Everything())
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("snapshot controller failed to list snapshots when enqueuing setting %v: %v",
			types.SettingNameDisableSnapshotPurge, err))
		return
	}
	for _, snap := range snapshots {
		if !snap.DeletionTimestamp.IsZero() {
			sc.enqueueSnapshot(snap)
		}
	}
}

func (sc *SnapshotController) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer sc.queue.ShutDown()

	sc.logger.Info("Starting Longhorn Snapshot Controller")
	defer sc.logger.Info("Shut down Longhorn Snapshot Controller")

	if !cache.WaitForNamedCacheSync(sc.name, stopCh, sc.cacheSyncs...) {
		return
	}

	for i := 0; i < workers; i++ {
		go wait.Until(sc.worker, time.Second, stopCh)
	}
	<-stopCh
}

func (sc *SnapshotController) worker() {
	for sc.processNextWorkItem() {
	}
}

func (sc *SnapshotController) processNextWorkItem() bool {
	key, quit := sc.queue.Get()
	if quit {
		return false
	}
	defer sc.queue.Done(key)
	err := sc.syncHandler(key.(string))
	sc.handleErr(err, key)
	return true
}

func (sc *SnapshotController) handleErr(err error, key interface{}) {
	if err == nil {
		sc.queue.Forget(key)
		return
	}

	log := sc.logger.WithField("Snapshot", key)
	handleReconcileErrorLogging(log, err, "Failed to sync Longhorn snapshot")
	sc.queue.AddRateLimited(key)
}

func (sc *SnapshotController) syncHandler(key string) (err error) {
	defer func() {
		err = errors.Wrapf(err, "%v: failed to sync snapshot %v", sc.name, key)
	}()

	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	if namespace != sc.namespace {
		return nil
	}
	return sc.reconcile(name)
}

func (sc *SnapshotController) reconcile(snapshotName string) (err error) {
	snapshot, err := sc.ds.GetSnapshot(snapshotName)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}

	isResponsible, err := sc.isResponsibleFor(snapshot)
	if err != nil {
		return err
	}
	if !isResponsible {
		return nil
	}

	existingSnapshot := snapshot.DeepCopy()
	defer func() {
		if err != nil && !shouldUpdateObject(err) {
			return
		}
		if reflect.DeepEqual(existingSnapshot.Status, snapshot.Status) {
			return
		}

		snapshot, err = sc.ds.UpdateSnapshotStatus(snapshot)
		if err != nil {
			return
		}
		sc.generatingEventsForSnapshot(existingSnapshot, snapshot)
	}()

	// deleting snapshotCR
	if !snapshot.DeletionTimestamp.IsZero() {
		isVolDeletedOrBeingDeleted, err := sc.isVolumeDeletedOrBeingDeleted(snapshot.Spec.Volume)
		if err != nil {
			return err
		}
		if isVolDeletedOrBeingDeleted {
			return sc.ds.RemoveFinalizerForSnapshot(snapshot)
		}

		engine, err := sc.getTheOnlyEngineCRforSnapshotRO(snapshot)
		if err != nil {
			return err
		}

		defer func() {
			engine, err = sc.ds.GetEngineRO(engine.Name)
			if err != nil {
				return
			}
			if snapshotInfo, ok := engine.Status.Snapshots[snapshot.Name]; ok {
				if err := syncSnapshotWithSnapshotInfo(snapshot, snapshotInfo, engine.Spec.VolumeSize); err != nil {
					return
				}
			}
		}()

		_, snapshotExistInEngineCR := engine.Status.Snapshots[snapshot.Name]
		if _, hasVolumeHeadChild := snapshot.Status.Children["volume-head"]; snapshotExistInEngineCR && hasVolumeHeadChild && snapshot.Status.MarkRemoved {
			// This snapshot is the parent of volume-head, so it cannot be purged immediately.
			// We do not want to keep the volume stuck in attached state.
			return sc.handleAttachmentTicketDeletion(snapshot)
		}

		disablePurge, err := sc.ds.GetSettingAsBool(types.SettingNameDisableSnapshotPurge)
		if err != nil {
			return err
		}
		if disablePurge {
			sc.logger.Warnf("Cannot start SnapshotPurge to delete snapshot %v while %v setting is true",
				snapshot.Name, types.SettingNameDisableSnapshotPurge)
			// Like above, we do not want to keep the volume stuck in an attached state if it is not possible to purge
			// this snapshot.
			return sc.handleAttachmentTicketDeletion(snapshot)
		}

		if err := sc.handleAttachmentTicketCreation(snapshot, true); err != nil {
			return err
		}

		if engine.Status.CurrentState != longhorn.InstanceStateRunning {
			return fmt.Errorf("failed to delete snapshot because the volume engine %v is not running. Will reenqueue and retry later", engine.Name)
		}
		// Delete the snapshot from engine process
		if err := sc.handleSnapshotDeletion(snapshot, engine); err != nil {
			return err
		}

		// Best-effort cleanup.
		// The child snapshot data content will change since the deleting parent snapshot data will be coalesced into its child.
		for childSnapshotName := range snapshot.Status.Children {
			if childSnapshotName == "volume-head" {
				continue
			}
			childSnapshot, err := sc.ds.GetSnapshot(childSnapshotName)
			if err != nil {
				sc.logger.WithError(err).Errorf("Failed to get the child snapshot %s during snapshot %s deletion", childSnapshotName, snapshotName)
				continue
			}
			if childSnapshot.Status.Checksum != "" {
				childSnapshot.Status.Checksum = ""
				if _, err = sc.ds.UpdateSnapshotStatus(childSnapshot); err != nil {
					sc.logger.WithError(err).Errorf("Failed to clean up the child snapshot %s checksum during snapshot %s deletion", childSnapshotName, snapshotName)
					continue
				}
			}
		}

		// Wait for the snapshot to be removed from engine.Status.Snapshots
		engine, err = sc.ds.GetEngineRO(engine.Name)
		if err != nil {
			return err
		}
		if _, ok := engine.Status.Snapshots[snapshot.Name]; !ok {
			if err = sc.handleAttachmentTicketDeletion(snapshot); err != nil {
				return err
			}
			return sc.ds.RemoveFinalizerForSnapshot(snapshot)
		}

		return nil
	}

	requestCreateNewSnapshot := snapshot.Spec.CreateSnapshot
	alreadyCreatedBefore := snapshot.Status.CreationTime != ""

	defer func() {
		if !requestCreateNewSnapshot || alreadyCreatedBefore {
			err = sc.handleAttachmentTicketDeletion(snapshot)
		}
	}()

	engine, err := sc.getTheOnlyEngineCRforSnapshotRO(snapshot)
	if err != nil {
		return err
	}

	// Skip handing new snapshot if they already exist in the engine CR.
	// The engine may be purging, and the snapshot may be deleted mid-reconciliation,
	// potentially lead to a mis-recreation.
	//
	// https://github.com/longhorn/longhorn/issues/10808
	snapshotExistInEngine := isSnapshotExistInEngine(snapshotName, engine)

	// Newly created snapshot CR by user
	if requestCreateNewSnapshot && !alreadyCreatedBefore && !snapshotExistInEngine {
		if err := sc.handleAttachmentTicketCreation(snapshot, false); err != nil {
			return err
		}
		if engine.Status.CurrentState != longhorn.InstanceStateRunning {
			snapshot.Status.Error = fmt.Sprintf("failed to take snapshot because the volume engine %v is not running. Waiting for the volume to be attached", engine.Name)
			return nil
		}
		err = sc.handleSnapshotCreate(snapshot, engine)
		if err != nil {
			snapshot.Status.Error = err.Error()
			return reconcileError{error: err, shouldUpdateObject: true}
		}
	}

	engine, err = sc.ds.GetEngineRO(engine.Name)
	if err != nil {
		return err
	}
	snapshotInfo, ok := engine.Status.Snapshots[snapshot.Name]
	if !ok {
		if !requestCreateNewSnapshot || alreadyCreatedBefore {
			// The snapshotInfo existed inside engine.Status.Snapshots before but is gone now. This often doesn't
			// signify an actual problem (e.g. if the snapshot is deleted by the engine process itself during a purge),
			// but the snapshot controller can't reconcile the status anymore. Add a message to the CR.
			snapshot.Status.Error = snapshotErrorLost
		}
		// Newly created snapshotCR, wait for the snapshotInfo to be appeared inside engine.Status.Snapshot
		snapshot.Status.ReadyToUse = false
		return nil
	}

	if err := syncSnapshotWithSnapshotInfo(snapshot, snapshotInfo, engine.Spec.VolumeSize); err != nil {
		return err
	}

	return nil
}

// handleAttachmentTicketDeletion check and delete attachment so that the source volume is detached if needed
func (sc *SnapshotController) handleAttachmentTicketDeletion(snap *longhorn.Snapshot) (err error) {
	defer func() {
		err = errors.Wrap(err, "handleAttachmentTicketDeletion: failed to clean up attachment")
	}()

	va, err := sc.ds.GetLHVolumeAttachmentByVolumeName(snap.Spec.Volume)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	attachmentTicketID := longhorn.GetAttachmentTicketID(longhorn.AttacherTypeSnapshotController, snap.Name)

	if _, ok := va.Spec.AttachmentTickets[attachmentTicketID]; ok {
		delete(va.Spec.AttachmentTickets, attachmentTicketID)
		if _, err = sc.ds.UpdateLHVolumeAttachment(va); err != nil {
			return err
		}
	}

	return nil
}

// handleAttachmentTicketCreation check and create attachment so that the source volume is attached if needed
func (sc *SnapshotController) handleAttachmentTicketCreation(snap *longhorn.Snapshot,
	checkIfSnapshotExists bool) (err error) {
	defer func() {
		err = errors.Wrap(err, "handleAttachmentTicketCreation: failed to create/update attachment")
	}()

	vol, err := sc.ds.GetVolumeRO(snap.Spec.Volume)
	if err != nil {
		return err
	}

	va, err := sc.ds.GetLHVolumeAttachmentByVolumeName(vol.Name)
	if err != nil {
		return err
	}

	existingVA := va.DeepCopy()
	defer func() {
		if reflect.DeepEqual(existingVA.Spec, va.Spec) {
			return
		}

		if checkIfSnapshotExists {
			// It is possible we are reconciling a cached version of a deleted snapshot (only if deletionTimestamp is
			// set). Confirm that the snapshot still exists on the API server before creating an attachment ticket to
			// prevent an orphan attachment (see longhorn/longhorn#6652). Avoid checking until here to limit
			// requests to the API server.
			if _, err = sc.ds.GetLonghornSnapshotUncached(snap.Name); err != nil {
				return // Either the snapshot no longer exists or we failed to check.
			}
		}

		if _, err = sc.ds.UpdateLHVolumeAttachment(va); err != nil {
			return
		}
	}()

	attachmentID := longhorn.GetAttachmentTicketID(longhorn.AttacherTypeSnapshotController, snap.Name)
	createOrUpdateAttachmentTicket(va, attachmentID, vol.Status.OwnerID, longhorn.AnyValue, longhorn.AttacherTypeSnapshotController)

	return nil
}

func (sc *SnapshotController) generatingEventsForSnapshot(existingSnapshot, snapshot *longhorn.Snapshot) {
	if !existingSnapshot.Status.MarkRemoved && snapshot.Status.MarkRemoved {
		sc.eventRecorder.Event(snapshot, corev1.EventTypeNormal, constant.EventReasonDelete, "snapshot is marked as removed")
	}
	if snapshot.Spec.CreateSnapshot && existingSnapshot.Status.CreationTime == "" && snapshot.Status.CreationTime != "" {
		sc.eventRecorder.Event(snapshot, corev1.EventTypeNormal, constant.EventReasonCreate, "successfully provisioned the snapshot")
	}
	if snapshot.Status.Error != "" && existingSnapshot.Status.Error != snapshot.Status.Error {
		if snapshot.Status.Error == snapshotErrorLost {
			// There are probably scenarios when this is an actual problem, so we want to continue to emit the event.
			// However, it most often occurs in scenarios like https://github.com/longhorn/longhorn/issues/4126, so we
			// want to use EventTypeNormal instead of EventTypeWarning.
			sc.eventRecorder.Event(snapshot, corev1.EventTypeNormal, constant.EventReasonDelete, "snapshot was removed from engine")
		} else {
			sc.eventRecorder.Eventf(snapshot, corev1.EventTypeWarning, constant.EventReasonFailed, "%v", snapshot.Status.Error)
		}
	}
	if existingSnapshot.Status.ReadyToUse != snapshot.Status.ReadyToUse {
		if snapshot.Status.ReadyToUse {
			sc.eventRecorder.Event(snapshot, corev1.EventTypeNormal, constant.EventReasonUpdate, "snapshot becomes ready to use")
		} else {
			sc.eventRecorder.Event(snapshot, corev1.EventTypeWarning, constant.EventReasonUpdate, "snapshot becomes not ready to use")
		}
	}
}

func (sc *SnapshotController) isVolumeDeletedOrBeingDeleted(volumeName string) (bool, error) {
	volume, err := sc.ds.GetVolumeRO(volumeName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return !volume.DeletionTimestamp.IsZero(), nil
}

func syncSnapshotWithSnapshotInfo(snap *longhorn.Snapshot, snapInfo *longhorn.SnapshotInfo, restoreSize int64) error {
	size, err := strconv.ParseInt(snapInfo.Size, 10, 64)
	if err != nil {
		return err
	}
	snap.Status.Parent = snapInfo.Parent
	snap.Status.Children = snapInfo.Children
	snap.Status.MarkRemoved = snapInfo.Removed
	snap.Status.UserCreated = snapInfo.UserCreated
	snap.Status.CreationTime = snapInfo.Created
	snap.Status.Size = size
	snap.Status.Labels = snapInfo.Labels
	snap.Status.RestoreSize = restoreSize
	if snap.Status.MarkRemoved || snap.DeletionTimestamp != nil {
		snap.Status.ReadyToUse = false
	} else {
		snap.Status.ReadyToUse = true
		snap.Status.Error = ""
	}
	return nil
}

func (sc *SnapshotController) getTheOnlyEngineCRforSnapshotRO(snapshot *longhorn.Snapshot) (*longhorn.Engine, error) {
	engines, err := sc.ds.ListVolumeEnginesRO(snapshot.Spec.Volume)
	if err != nil {
		return nil, errors.Wrap(err, "getTheOnlyEngineCRforSnapshot")
	}
	if len(engines) != 1 {
		return nil, fmt.Errorf("getTheOnlyEngineCRforSnapshot: found more than 1 engines for volume %v", snapshot.Spec.Volume)
	}
	var engine *longhorn.Engine
	for _, e := range engines {
		engine = e
		break
	}
	return engine, nil
}

func (sc *SnapshotController) handleSnapshotCreate(snapshot *longhorn.Snapshot, engine *longhorn.Engine) error {
	freezeFilesystem, err := sc.ds.GetFreezeFilesystemForSnapshotSetting(engine)
	if err != nil {
		return err
	}

	engineCliClient, err := GetBinaryClientForEngine(engine, sc.engineClientCollection, engine.Status.CurrentImage)
	if err != nil {
		return err
	}

	engineClientProxy, err := engineapi.GetCompatibleClient(engine, engineCliClient, sc.ds, sc.logger, sc.proxyConnCounter)
	if err != nil {
		return err
	}
	defer engineClientProxy.Close()

	snapshotInfo, err := engineClientProxy.SnapshotGet(engine, snapshot.Name)
	if err != nil {
		return err
	}
	if snapshotInfo == nil {
		sc.logger.Infof("Creating snapshot %v of volume %v", snapshot.Name, snapshot.Spec.Volume)
		_, err = engineClientProxy.SnapshotCreate(engine, snapshot.Name, snapshot.Spec.Labels, freezeFilesystem)
		if err != nil {
			return err
		}
	}
	return nil
}

// handleSnapshotDeletion reaches out to engine process to check and delete the snapshot
func (sc *SnapshotController) handleSnapshotDeletion(snapshot *longhorn.Snapshot, engine *longhorn.Engine) error {
	engineCliClient, err := GetBinaryClientForEngine(engine, sc.engineClientCollection, engine.Status.CurrentImage)
	if err != nil {
		return err
	}
	engineClientProxy, err := engineapi.GetCompatibleClient(engine, engineCliClient, sc.ds, sc.logger, sc.proxyConnCounter)
	if err != nil {
		return err
	}
	defer engineClientProxy.Close()

	snapshotInfo, err := engineClientProxy.SnapshotGet(engine, snapshot.Name)
	if err != nil {
		return err
	}
	if snapshotInfo == nil {
		return nil
	}

	if !snapshotInfo.Removed {
		sc.logger.Infof("Deleting snapshot %v", snapshot.Name)
		if err = engineClientProxy.SnapshotDelete(engine, snapshot.Name); err != nil {
			return err
		}
	}
	// TODO: Check if the purge failure is handled somewhere else
	purgeStatus, err := engineClientProxy.SnapshotPurgeStatus(engine)
	if err != nil {
		return errors.Wrap(err, "failed to get snapshot purge status")
	}
	isPurging := false
	for _, status := range purgeStatus {
		if status.IsPurging {
			isPurging = true
			break
		}
	}
	if !isPurging {
		// We checked DisableSnapshotPurge at a higher level, so we do not need to check it again here.
		sc.logger.Infof("Starting SnapshotPurge to delete snapshot %v", snapshot.Name)
		if err := engineClientProxy.SnapshotPurge(engine); err != nil {
			return err
		}
	}

	return nil
}

func (sc *SnapshotController) isResponsibleFor(snap *longhorn.Snapshot) (bool, error) {
	var err error
	defer func() {
		err = errors.Wrap(err, "error while checking isResponsibleFor")
	}()

	volume, err := sc.ds.GetVolumeRO(snap.Spec.Volume)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return sc.controllerID == volume.Status.OwnerID, nil
}

func getLoggerForSnapshot(logger logrus.FieldLogger, snap *longhorn.Snapshot) *logrus.Entry {
	return logger.WithFields(
		logrus.Fields{
			"snapshot": snap.Name,
		},
	)
}

type reconcileError struct {
	error
	shouldUpdateObject bool
}

func shouldUpdateObject(err error) bool {
	switch v := err.(type) {
	case reconcileError:
		return v.shouldUpdateObject
	}
	return false
}
