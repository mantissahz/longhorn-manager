package controller

import (
	"fmt"
	"reflect"
	"time"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/kubernetes/pkg/controller"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientset "k8s.io/client-go/kubernetes"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/longhorn/backupstore"

	"github.com/longhorn/longhorn-manager/datastore"
	"github.com/longhorn/longhorn-manager/types"
	"github.com/longhorn/longhorn-manager/util"

	longhorn "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta2"
)

type BackupVolumeController struct {
	*baseController

	// which namespace controller is running with
	namespace string
	// use as the OwnerID of the controller
	controllerID string

	kubeClient    clientset.Interface
	eventRecorder record.EventRecorder

	ds *datastore.DataStore

	cacheSyncs []cache.InformerSynced

	proxyConnCounter util.Counter
}

func NewBackupVolumeController(
	logger logrus.FieldLogger,
	ds *datastore.DataStore,
	scheme *runtime.Scheme,
	kubeClient clientset.Interface,
	controllerID string,
	namespace string,
	proxyConnCounter util.Counter,
) *BackupVolumeController {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(logrus.Infof)
	// TODO: remove the wrapper when every clients have moved to use the clientset.
	eventBroadcaster.StartRecordingToSink(&v1core.EventSinkImpl{
		Interface: v1core.New(kubeClient.CoreV1().RESTClient()).Events(""),
	})

	bvc := &BackupVolumeController{
		baseController: newBaseController("longhorn-backup-volume", logger),

		namespace:    namespace,
		controllerID: controllerID,

		ds: ds,

		kubeClient:    kubeClient,
		eventRecorder: eventBroadcaster.NewRecorder(scheme, corev1.EventSource{Component: "longhorn-backup-volume-controller"}),

		proxyConnCounter: proxyConnCounter,
	}

	ds.BackupVolumeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    bvc.enqueueBackupVolume,
		UpdateFunc: func(old, cur interface{}) { bvc.enqueueBackupVolume(cur) },
		DeleteFunc: bvc.enqueueBackupVolume,
	})
	bvc.cacheSyncs = append(bvc.cacheSyncs, ds.BackupVolumeInformer.HasSynced)

	return bvc
}

func (bvc *BackupVolumeController) enqueueBackupVolume(obj interface{}) {
	key, err := controller.KeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %v", obj, err))
		return
	}

	bvc.queue.Add(key)
}

func (bvc *BackupVolumeController) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer bvc.queue.ShutDown()

	bvc.logger.Info("Starting Longhorn Backup Volume controller")
	defer bvc.logger.Info("Shut down Longhorn Backup Volume controller")

	if !cache.WaitForNamedCacheSync(bvc.name, stopCh, bvc.cacheSyncs...) {
		return
	}
	for i := 0; i < workers; i++ {
		go wait.Until(bvc.worker, time.Second, stopCh)
	}
	<-stopCh
}

func (bvc *BackupVolumeController) worker() {
	for bvc.processNextWorkItem() {
	}
}

func (bvc *BackupVolumeController) processNextWorkItem() bool {
	key, quit := bvc.queue.Get()
	if quit {
		return false
	}
	defer bvc.queue.Done(key)
	err := bvc.syncHandler(key.(string))
	bvc.handleErr(err, key)
	return true
}

func (bvc *BackupVolumeController) syncHandler(key string) (err error) {
	defer func() {
		err = errors.Wrapf(err, "%v: failed to sync backup volume %v", bvc.name, key)
	}()

	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	if namespace != bvc.namespace {
		// Not ours, skip it
		return nil
	}
	return bvc.reconcile(name)
}

func (bvc *BackupVolumeController) handleErr(err error, key interface{}) {
	if err == nil {
		bvc.queue.Forget(key)
		return
	}

	if bvc.queue.NumRequeues(key) < maxRetries {
		bvc.logger.WithError(err).Errorf("Failed to sync Longhorn backup volume %v", key)
		bvc.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	bvc.logger.WithError(err).Errorf("Dropping Longhorn backup volume %v out of the queue", key)
	bvc.queue.Forget(key)
}

func getLoggerForBackupVolume(logger logrus.FieldLogger, backupVolume *longhorn.BackupVolume) *logrus.Entry {
	return logger.WithFields(
		logrus.Fields{
			"backupVolume": backupVolume.Name,
		},
	)
}

func (bvc *BackupVolumeController) reconcile(backupVolumeName string) (err error) {
	// Get BackupVolume CR
	backupVolume, err := bvc.ds.GetBackupVolume(backupVolumeName)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}

	// Check the responsible node
	defaultEngineImage, err := bvc.ds.GetSettingValueExisted(types.SettingNameDefaultEngineImage)
	if err != nil {
		return err
	}
	isResponsible, err := bvc.isResponsibleFor(backupVolume, defaultEngineImage)
	if err != nil {
		return nil
	}
	if !isResponsible {
		return nil
	}
	if backupVolume.Status.OwnerID != bvc.controllerID {
		backupVolume.Status.OwnerID = bvc.controllerID
		backupVolume, err = bvc.ds.UpdateBackupVolumeStatus(backupVolume)
		if err != nil {
			// we don't mind others coming first
			if apierrors.IsConflict(errors.Cause(err)) {
				return nil
			}
			return err
		}
	}

	log := getLoggerForBackupVolume(bvc.logger, backupVolume)
	log.Warnf("[James_DBG] BackupVolumeCtlr isResponsibleFor contorller: %v", backupVolume.Status.OwnerID)

	// Get default backup target
	// backupTarget, err := bvc.ds.GetBackupTargetRO(types.DefaultBackupTargetName)
	log.Warnf("[James_DBG] BackupVolumeCtlr backupVolume.Spec.BackupTargetURL: %v", backupVolume.Spec.BackupTargetURL)
	backupTarget, err := bvc.ds.GetBackupTargetRO(backupVolume.Spec.BackupTargetName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Warnf("[James_DBG] GetBackupTargetRO backup.Spec.BackupTargetName: %v not found", backupVolume.Spec.BackupTargetName)
			return nil
		}
		return errors.Wrapf(err, "failed to get %s backup target", backupVolume.Spec.BackupTargetName)
	}

	// Examine DeletionTimestamp to determine if object is under deletion
	if !backupVolume.DeletionTimestamp.IsZero() {
		log.Warnf("[James_DBG] BackupVolumeCtlr delete BV volumeName: %v", backupVolume.Spec.VolumeName)
		backupsOfVolume, err := bvc.ds.ListBackupsWithBackupVolumeName(backupVolume.Spec.VolumeName)
		if err != nil {
			return errors.Wrap(err, "failed to get backups by volume name")
		}
		log.Warnf("[James_DBG] BackupVolumeCtlr delete BV backupsOfVolume: %v", backupsOfVolume)

		for backupName := range backupsOfVolume {
			log.Warnf("[James_DBG] BackupVolumeCtlr delete BV delete backup: %v", backupName)
			backupObj, err := bvc.ds.GetBackupRO(backupName)
			if err != nil {
				return errors.Wrap(err, "failed to get the backup")
			}
			if backupObj.Spec.BackupTargetURL != backupVolume.Spec.BackupTargetURL {
				continue
			}
			log.Warnf("[James_DBG] BackupVolumeCtlr delete BV Truly delete backup: %v", backupName)
			if err := bvc.ds.DeleteBackup(backupName); err != nil {
				return errors.Wrap(err, "failed to delete backups")
			}
		}

		// Delete the backup volume from the remote backup target
		log.Warnf("[James_DBG] BackupVolumeCtlr delete BV backupTarget.DeletionTimestamp.IsZero(): %v, backupTarget.Spec.BackupTargetURL: %v", backupTarget.DeletionTimestamp.IsZero(), backupTarget.Spec.BackupTargetURL)
		if backupTarget != nil && backupTarget.DeletionTimestamp == nil && backupTarget.Spec.BackupTargetURL != "" {
			log.Warnf("[James_DBG] BackupVolumeCtlr delete BV backupTarget.DeletionTimestamp == nil")
			engineClientProxy, backupTargetClient, err := getBackupTarget(bvc.controllerID, backupTarget, bvc.ds, log, bvc.proxyConnCounter)
			if err != nil || engineClientProxy == nil {
				log.WithError(err).Error("Failed to init backup target clients")
				return nil // Ignore error to prevent enqueue
			}
			defer engineClientProxy.Close()

			log.Warnf("[James_DBG] BackupVolumeCtlr reconcile BVDelete delete remote backupVolumeName: %v, volumeName: %v", backupVolumeName, backupVolume.Spec.VolumeName)
			if err := backupTargetClient.BackupVolumeDelete(backupTargetClient.URL, backupVolume.Spec.VolumeName, backupTargetClient.Credential); err != nil {
				return errors.Wrap(err, "failed to delete remote backup volume")
			}
		}

		return bvc.ds.RemoveFinalizerForBackupVolume(backupVolume)
	}

	syncTime := metav1.Time{Time: time.Now().UTC()}
	existingBackupVolume := backupVolume.DeepCopy()
	defer func() {
		if err != nil {
			return
		}
		if reflect.DeepEqual(existingBackupVolume.Status, backupVolume.Status) {
			return
		}
		if _, err := bvc.ds.UpdateBackupVolumeStatus(backupVolume); err != nil && apierrors.IsConflict(errors.Cause(err)) {
			log.WithError(err).Debugf("Requeue %v due to conflict", backupVolumeName)
			bvc.enqueueBackupVolume(backupVolume)
		}
	}()

	// Check the controller should run synchronization
	log.Warnf("[James_DBG] BackupVolumeCtlr backupVolume.Status.LastSyncedAt.IsZero(): %v", backupVolume.Status.LastSyncedAt.IsZero())
	if !backupVolume.Status.LastSyncedAt.IsZero() &&
		!backupVolume.Spec.SyncRequestedAt.After(backupVolume.Status.LastSyncedAt.Time) {
		return nil
	}

	log.Warnf("[James_DBG] BackupVolumeCtlr getBackupTarget")
	engineClientProxy, backupTargetClient, err := getBackupTarget(bvc.controllerID, backupTarget, bvc.ds, log, bvc.proxyConnCounter)
	if err != nil {
		log.WithError(err).Error("Failed to init backup target clients")
		return nil // Ignore error to prevent enqueue
	}
	defer engineClientProxy.Close()

	// Get a list of all the backups that are stored in the backup target
	log.Warnf("[James_DBG] BackupVolumeCtlr BackupNameList by VolumeName: %v", backupVolume.Spec.VolumeName)
	res, err := backupTargetClient.BackupNameList(backupTargetClient.URL, backupVolume.Spec.VolumeName, backupTargetClient.Credential)
	if err != nil {
		log.WithError(err).Error("Failed to list backups from backup target")
		return nil // Ignore error to prevent enqueue
	}
	backupStoreBackups := sets.NewString(res...)

	// Get a list of all the backups that exist as custom resources in the cluster
	clusterBackups, err := bvc.ds.ListBackupsWithBackupVolumeName(backupVolume.Spec.VolumeName)
	if err != nil {
		log.WithError(err).Error("Failed to list backups in the cluster")
		return err
	}
	log.Warnf("[James_DBG] BackupVolumeCtlr reconcile ListBackupsWithBackupVolumeName: %+v", clusterBackups)

	// Get `Time to live` after failed backup was marked as `Error` or `Unknown`,
	failedBackupTTL, err := bvc.ds.GetSettingAsInt(types.SettingNameFailedBackupTTL)
	if err != nil {
		log.WithError(err).Warnf("Failed to get %v setting, and it will skip the auto-deletion for the failed backups", types.SettingNameFailedBackupTTL)
	}
	clustersSet := sets.NewString()
	for _, b := range clusterBackups {
		// Skip other backup target backups from the same volume
		log.Warnf("[James_DBG] BackupVolumeCtlr reconcile b.Spec.BackupTargetName: %v, backupVolume.Spec.BackupTargetName: %v", b.Spec.BackupTargetURL, backupVolume.Spec.BackupTargetURL)
		if b.Spec.BackupTargetURL != backupVolume.Spec.BackupTargetURL {
			log.Warnf("[James_DBG] BackupVolumeCtlr reconcile b.Spec.BackupTargetName != backupVolume.Spec.BackupTargetName, Skipped")
			continue
		}
		// Skip the Backup CR which is created from the local cluster and
		// the snapshot backup hasn't be completed or pulled from the remote backup target yet
		if b.Spec.SnapshotName != "" && b.Status.State != longhorn.BackupStateCompleted {
			if b.Status.State == longhorn.BackupStateError || b.Status.State == longhorn.BackupStateUnknown {
				// Failed backup `LastSyncedAt` should not be updated after it was marked as `Error` or `Unknown`
				if failedBackupTTL > 0 && time.Now().After(b.Status.LastSyncedAt.Add(time.Duration(failedBackupTTL)*time.Minute)) {
					if err = bvc.ds.DeleteBackup(b.Name); err != nil {
						log.WithError(err).Errorf("Failed to delete failed backup %s", b.Name)
					}
				}
			}
			continue
		}
		clustersSet.Insert(b.Name)
	}

	// Get a list of backups that *are* in the backup target and *aren't* in the cluster
	// and create the Backup CR in the cluster
	backupsToPull := backupStoreBackups.Difference(clustersSet)
	log.Warnf("[James_DBG] BackupVolumeCtlr reconcile backupsToPull: %+v", backupsToPull)
	if count := backupsToPull.Len(); count > 0 {
		log.Infof("Found %d backups in the backup target that do not exist in the cluster and need to be pulled", count)
	}
	for backupName := range backupsToPull {
		backupLabelMap := map[string]string{}

		backupURL := backupstore.EncodeBackupURL(backupName, backupVolume.Spec.VolumeName, backupTargetClient.URL)
		if backupInfo, err := backupTargetClient.BackupGet(backupURL, backupTargetClient.Credential); err != nil {
			log.WithError(err).WithFields(logrus.Fields{
				"backup":       backupName,
				"backupvolume": backupVolumeName,
				"backuptarget": backupURL}).Warn("Failed to get backupInfo from remote backup target")
		} else {
			if accessMode, exist := backupInfo.Labels[types.GetLonghornLabelKey(types.LonghornLabelVolumeAccessMode)]; exist {
				backupLabelMap[types.GetLonghornLabelKey(types.LonghornLabelVolumeAccessMode)] = accessMode
			}
		}

		backup := &longhorn.Backup{
			ObjectMeta: metav1.ObjectMeta{
				Name: backupName,
				Labels: map[string]string{
					types.LonghornLabelBackupTarget: backupTarget.Name,
				},
			},
			Spec: longhorn.BackupSpec{
				Labels:           backupLabelMap,
				BackupTargetURL:  backupVolume.Spec.BackupTargetURL,
				BackupTargetName: backupVolume.Spec.BackupTargetName,
			},
		}
		if _, err = bvc.ds.CreateBackup(backup, backupVolume.Spec.VolumeName); err != nil && !apierrors.IsAlreadyExists(err) {
			log.WithError(err).Errorf("Failed to create backup %s in the cluster", backupName)
			return err
		}
	}

	// Get a list of backups that *are* in the cluster and *aren't* in the backup target
	// and delete the Backup CR in the cluster
	backupsToDelete := clustersSet.Difference(backupStoreBackups)
	log.Warnf("[James_DBG] BackupVolumeCtlr reconcile backupsToDelete: %+v", backupsToDelete)
	if count := backupsToDelete.Len(); count > 0 {
		log.Infof("Found %d backups in the backup target that do not exist in the backup target and need to be deleted", count)
	}
	for backupName := range backupsToDelete {
		if err = bvc.ds.DeleteBackup(backupName); err != nil {
			return errors.Wrapf(err, "failed to delete backup %s from cluster", backupName)
		}
	}

	backupVolumeMetadataURL := backupstore.EncodeBackupURL("", backupVolume.Spec.VolumeName, backupTargetClient.URL)
	log.Warnf("[James_DBG] BackupVolumeCtlr reconcile backupVolumeMetadataURL: %v", backupVolumeMetadataURL)
	configMetadata, err := backupTargetClient.BackupConfigMetaGet(backupVolumeMetadataURL, backupTargetClient.Credential)
	if err != nil {
		log.WithError(err).Error("Failed to get backup volume config metadata from backup target")
		return nil // Ignore error to prevent enqueue
	}
	log.Warnf("[James_DBG] BackupVolumeCtlr reconcile configMetadata: %+v", configMetadata)
	if configMetadata == nil {
		return nil
	}

	// If there is no backup CR creation/deletion and the backup volume config metadata not changed
	// skip read the backup volume config
	if len(backupsToPull) == 0 && len(backupsToDelete) == 0 &&
		backupVolume.Status.LastModificationTime.Time.Equal(configMetadata.ModificationTime) {
		log.Warnf("[James_DBG] BackupVolumeCtlr reconcile sync and return")
		backupVolume.Status.LastSyncedAt = syncTime
		return nil
	}

	log.Warnf("[James_DBG] BackupVolumeCtlr reconcile BackupVolumeGet from backupVolumeMetadataURL: %v", backupVolumeMetadataURL)
	backupVolumeInfo, err := backupTargetClient.BackupVolumeGet(backupVolumeMetadataURL, backupTargetClient.Credential)
	if err != nil {
		log.WithError(err).Error("Failed to get backup volume config from backup target")
		return nil // Ignore error to prevent enqueue
	}
	log.Warnf("[James_DBG] BackupVolumeCtlr reconcile backupVolumeInfo: %v", backupVolumeInfo)
	if backupVolumeInfo == nil {
		return nil
	}

	// Update the Backup CR spec.syncRequestAt to request the
	// backup_controller to reconcile the Backup CR if the last backup changed
	log.Warnf("[James_DBG] BackupVolumeCtlr reconcile backupVolume.Status.LastBackupName: %v, backupVolumeInfo.LastBackupName: %v", backupVolume.Status.LastBackupName, backupVolumeInfo.LastBackupName)
	if backupVolume.Status.LastBackupName != backupVolumeInfo.LastBackupName {
		backup, err := bvc.ds.GetBackup(backupVolumeInfo.LastBackupName)
		if err == nil {
			backup.Spec.SyncRequestedAt = syncTime
			log.Warnf("[James_DBG] BackupVolumeCtlr reconcile backup(%v): %+v", backup.Name, backup.Spec)
			if _, err = bvc.ds.UpdateBackup(backup); err != nil && !apierrors.IsConflict(errors.Cause(err)) {
				log.WithError(err).Errorf("Failed to update backup %s spec", backup.Name)
			}
		}
	}

	// Update BackupVolume CR status
	log.Warnf("[James_DBG] BackupVolumeCtlr reconcile sync backupVolume status from backupVolumeInfo")
	backupVolume.Status.LastModificationTime = metav1.Time{Time: configMetadata.ModificationTime}
	backupVolume.Status.Size = backupVolumeInfo.Size
	backupVolume.Status.Labels = backupVolumeInfo.Labels
	backupVolume.Status.CreatedAt = backupVolumeInfo.Created
	backupVolume.Status.LastBackupName = backupVolumeInfo.LastBackupName
	backupVolume.Status.LastBackupAt = backupVolumeInfo.LastBackupAt
	backupVolume.Status.DataStored = backupVolumeInfo.DataStored
	backupVolume.Status.Messages = backupVolumeInfo.Messages
	backupVolume.Status.BackingImageName = backupVolumeInfo.BackingImageName
	backupVolume.Status.BackingImageChecksum = backupVolumeInfo.BackingImageChecksum
	backupVolume.Status.StorageClassName = backupVolumeInfo.StorageClassName
	backupVolume.Status.LastSyncedAt = syncTime
	return nil
}

func (bvc *BackupVolumeController) isResponsibleFor(bv *longhorn.BackupVolume, defaultEngineImage string) (bool, error) {
	var err error
	defer func() {
		err = errors.Wrap(err, "error while checking isResponsibleFor")
	}()

	isResponsible := isControllerResponsibleFor(bvc.controllerID, bvc.ds, bv.Name, "", bv.Status.OwnerID)

	currentOwnerEngineAvailable, err := bvc.ds.CheckEngineImageReadiness(defaultEngineImage, bv.Status.OwnerID)
	if err != nil {
		return false, err
	}
	currentNodeEngineAvailable, err := bvc.ds.CheckEngineImageReadiness(defaultEngineImage, bvc.controllerID)
	if err != nil {
		return false, err
	}

	isPreferredOwner := currentNodeEngineAvailable && isResponsible
	continueToBeOwner := currentNodeEngineAvailable && bvc.controllerID == bv.Status.OwnerID
	requiresNewOwner := currentNodeEngineAvailable && !currentOwnerEngineAvailable

	return isPreferredOwner || continueToBeOwner || requiresNewOwner, nil
}
