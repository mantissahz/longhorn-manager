package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/kubernetes/pkg/controller"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientset "k8s.io/client-go/kubernetes"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/longhorn/longhorn-manager/datastore"
	"github.com/longhorn/longhorn-manager/engineapi"
	"github.com/longhorn/longhorn-manager/types"
	"github.com/longhorn/longhorn-manager/util"

	longhorn "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta2"
)

var (
	ownerKindEngineImage = longhorn.SchemeGroupVersion.WithKind("EngineImage").String()

	ExpiredEngineImageTimeout = 60 * time.Minute
)

type EngineImageController struct {
	*baseController

	// which namespace controller is running with
	namespace string
	// use as the OwnerID of the engine image
	controllerID   string
	serviceAccount string

	kubeClient    clientset.Interface
	eventRecorder record.EventRecorder

	ds *datastore.DataStore

	cacheSyncs []cache.InformerSynced

	// for unit test
	nowHandler                func() string
	engineBinaryChecker       func(string) (bool, error)
	engineImageVersionUpdater func(*longhorn.EngineImage) error
}

func NewEngineImageController(
	logger logrus.FieldLogger,
	ds *datastore.DataStore,
	scheme *runtime.Scheme,
	kubeClient clientset.Interface,
	namespace string, controllerID, serviceAccount string) (*EngineImageController, error) {

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(logrus.Infof)
	// TODO: remove the wrapper when every clients have moved to use the clientset.
	eventBroadcaster.StartRecordingToSink(&v1core.EventSinkImpl{Interface: v1core.New(kubeClient.CoreV1().RESTClient()).Events("")})

	ic := &EngineImageController{
		baseController: newBaseController("longhorn-engine-image", logger),

		namespace:      namespace,
		controllerID:   controllerID,
		serviceAccount: serviceAccount,

		kubeClient:    kubeClient,
		eventRecorder: eventBroadcaster.NewRecorder(scheme, corev1.EventSource{Component: "longhorn-engine-image-controller"}),

		ds: ds,

		nowHandler:                util.Now,
		engineBinaryChecker:       types.EngineBinaryExistOnHostForImage,
		engineImageVersionUpdater: updateEngineImageVersion,
	}

	var err error
	if _, err = ds.EngineImageInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    ic.enqueueEngineImage,
		UpdateFunc: func(old, cur interface{}) { ic.enqueueEngineImage(cur) },
		DeleteFunc: ic.enqueueEngineImage,
	}); err != nil {
		return nil, err
	}
	ic.cacheSyncs = append(ic.cacheSyncs, ds.EngineImageInformer.HasSynced)

	if _, err = ds.VolumeInformer.AddEventHandlerWithResyncPeriod(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { ic.enqueueVolumes(obj) },
		UpdateFunc: func(old, cur interface{}) { ic.enqueueVolumes(old, cur) },
		DeleteFunc: func(obj interface{}) { ic.enqueueVolumes(obj) },
	}, 0); err != nil {
		return nil, err
	}
	ic.cacheSyncs = append(ic.cacheSyncs, ds.VolumeInformer.HasSynced)

	if _, err = ds.DaemonSetInformer.AddEventHandlerWithResyncPeriod(cache.ResourceEventHandlerFuncs{
		AddFunc:    ic.enqueueControlleeChange,
		UpdateFunc: func(old, cur interface{}) { ic.enqueueControlleeChange(cur) },
		DeleteFunc: ic.enqueueControlleeChange,
	}, 0); err != nil {
		return nil, err
	}
	ic.cacheSyncs = append(ic.cacheSyncs, ds.DaemonSetInformer.HasSynced)

	return ic, nil
}

func (ic *EngineImageController) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer ic.queue.ShutDown()

	ic.logger.Info("Starting Longhorn Engine Image controller")
	defer ic.logger.Info("Shut down Longhorn Engine Image controller")

	if !cache.WaitForNamedCacheSync("longhorn engine images", stopCh, ic.cacheSyncs...) {
		return
	}

	for i := 0; i < workers; i++ {
		go wait.Until(ic.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (ic *EngineImageController) worker() {
	for ic.processNextWorkItem() {
	}
}

func (ic *EngineImageController) processNextWorkItem() bool {
	key, quit := ic.queue.Get()

	if quit {
		return false
	}
	defer ic.queue.Done(key)

	err := ic.syncEngineImage(key.(string))
	ic.handleErr(err, key)

	return true
}

func (ic *EngineImageController) handleErr(err error, key interface{}) {
	if err == nil {
		ic.queue.Forget(key)
		return
	}

	log := ic.logger.WithField("engineImage", key)
	if ic.queue.NumRequeues(key) < maxRetries {
		handleReconcileErrorLogging(log, err, "Failed to sync Longhorn engine image")
		ic.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	handleReconcileErrorLogging(log, err, "Dropping Longhorn engine image out of the queue")
	ic.queue.Forget(key)
}

func getLoggerForEngineImage(logger logrus.FieldLogger, ei *longhorn.EngineImage) *logrus.Entry {
	return logger.WithFields(
		logrus.Fields{
			"engineImage": ei.Name,
			"image":       ei.Spec.Image,
		},
	)
}

func (ic *EngineImageController) syncEngineImage(key string) (err error) {
	defer func() {
		err = errors.Wrapf(err, "failed to sync engine image for %v", key)
	}()
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	if namespace != ic.namespace {
		// Not ours, don't do anything
		return nil
	}
	engineImage, err := ic.ds.GetEngineImage(name)
	if err != nil {
		if datastore.ErrorIsNotFound(err) {
			return nil
		}
		return errors.Wrapf(err, "failed to get engine image")
	}
	log := getLoggerForEngineImage(ic.logger, engineImage)

	// check isResponsibleFor here
	isResponsible, err := ic.isResponsibleFor(engineImage)
	if err != nil {
		return err
	}
	if !isResponsible {
		return nil
	}

	if engineImage.Status.OwnerID != ic.controllerID {
		engineImage.Status.OwnerID = ic.controllerID
		engineImage, err = ic.ds.UpdateEngineImageStatus(engineImage)
		if err != nil {
			// we don't mind others coming first
			if apierrors.IsConflict(errors.Cause(err)) {
				return nil
			}
			return err
		}
		log.Infof("Engine Image got new owner %v", ic.controllerID)
	}

	checksumName := types.GetEngineImageChecksumName(engineImage.Spec.Image)
	if engineImage.Name != checksumName {
		return fmt.Errorf("image %v checksum name %v doesn't match engine image name %v", engineImage.Spec.Image, checksumName, engineImage.Name)
	}

	dsName := types.GetDaemonSetNameFromEngineImageName(engineImage.Name)
	if engineImage.DeletionTimestamp != nil {
		return ic.ds.RemoveFinalizerForEngineImage(engineImage)
	}

	existingEngineImage := engineImage.DeepCopy()
	defer func() {
		if err == nil && !reflect.DeepEqual(existingEngineImage.Status, engineImage.Status) {
			_, err = ic.ds.UpdateEngineImageStatus(engineImage)
		}
		if apierrors.IsConflict(errors.Cause(err)) {
			log.WithError(err).Debugf("Requeue %v due to conflict", key)
			ic.enqueueEngineImage(engineImage)
			err = nil
		}
	}()

	ds, err := ic.ds.GetEngineImageDaemonSet(dsName)
	if err != nil {
		return errors.Wrapf(err, "cannot get daemonset for engine image %v", engineImage.Name)
	}
	if ds == nil {
		tolerations, err := ic.ds.GetSettingTaintToleration()
		if err != nil {
			return errors.Wrapf(err, "failed to get taint toleration setting before creating engine image daemonset")
		}

		nodeSelector, err := ic.ds.GetSettingSystemManagedComponentsNodeSelector()
		if err != nil {
			return err
		}

		priorityClassSetting, err := ic.ds.GetSettingWithAutoFillingRO(types.SettingNamePriorityClass)
		if err != nil {
			return errors.Wrapf(err, "failed to get priority class setting before creating engine image daemonset")
		}
		priorityClass := priorityClassSetting.Value

		registrySecretSetting, err := ic.ds.GetSettingWithAutoFillingRO(types.SettingNameRegistrySecret)
		if err != nil {
			return errors.Wrapf(err, "failed to get registry secret setting before creating engine image daemonset")
		}
		registrySecret := registrySecretSetting.Value

		imagePullPolicy, err := ic.ds.GetSettingImagePullPolicy()
		if err != nil {
			return errors.Wrapf(err, "failed to get system pods image pull policy before creating engine image daemonset")
		}

		dsSpec, err := ic.createEngineImageDaemonSetSpec(engineImage, tolerations, priorityClass, registrySecret, imagePullPolicy, nodeSelector)
		if err != nil {
			return errors.Wrapf(err, "failed to create daemonset spec for engine image %v", engineImage.Name)
		}

		log.Infof("Creating daemon set %v for engine image %v (%v)", dsSpec.Name, engineImage.Name, engineImage.Spec.Image)
		if err = ic.ds.CreateEngineImageDaemonSet(dsSpec); err != nil {
			return errors.Wrapf(err, "failed to create daemonset for engine image %v", engineImage.Name)
		}

		engineImage.Status.Conditions = types.SetCondition(engineImage.Status.Conditions,
			longhorn.EngineImageConditionTypeReady, longhorn.ConditionStatusFalse,
			longhorn.EngineImageConditionTypeReadyReasonDaemonSet, fmt.Sprintf("creating daemon set %v for %v", dsSpec.Name, engineImage.Spec.Image))
		engineImage.Status.State = longhorn.EngineImageStateDeploying
		return nil
	}

	// TODO: Will remove this reference kind correcting after all Longhorn components having used the new kinds
	if len(ds.OwnerReferences) < 1 || ds.OwnerReferences[0].Kind != types.LonghornKindEngineImage {
		ds.OwnerReferences = datastore.GetOwnerReferencesForEngineImage(engineImage)
		_, err = ic.kubeClient.AppsV1().DaemonSets(ic.namespace).Update(context.TODO(), ds, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	}

	if err := ic.updateEngineImageRefCount(engineImage); err != nil {
		return errors.Wrapf(err, "failed to update RefCount for engine image %v(%v)", engineImage.Name, engineImage.Spec.Image)
	}

	if err := ic.cleanupExpiredEngineImage(engineImage); err != nil {
		return err
	}

	if err := ic.syncNodeDeploymentMap(engineImage); err != nil {
		return err
	}

	ok, err := ic.engineBinaryChecker(engineImage.Spec.Image)
	if !ok {
		engineImage.Status.Conditions = types.SetCondition(engineImage.Status.Conditions, longhorn.EngineImageConditionTypeReady, longhorn.ConditionStatusFalse, longhorn.EngineImageConditionTypeReadyReasonDaemonSet, errors.Errorf("engine binary check failed: %v", err).Error())
		engineImage.Status.State = longhorn.EngineImageStateDeploying
		return nil
	}

	if err := ic.engineImageVersionUpdater(engineImage); err != nil {
		return err
	}

	if err := engineapi.CheckCLICompatibility(engineImage.Status.CLIAPIVersion, engineImage.Status.CLIAPIMinVersion); err != nil {
		engineImage.Status.Conditions = types.SetCondition(engineImage.Status.Conditions, longhorn.EngineImageConditionTypeReady, longhorn.ConditionStatusFalse, longhorn.EngineImageConditionTypeReadyReasonBinary, "incompatible")
		engineImage.Status.Incompatible = true
		return nil
	}

	deployedNodeCount := 0
	for _, isDeployed := range engineImage.Status.NodeDeploymentMap {
		if isDeployed {
			deployedNodeCount++
		}
	}

	readyNodes, err := ic.ds.ListReadyNodesRO()
	if err != nil {
		return err
	}

	if deployedNodeCount < len(readyNodes) {
		engineImage.Status.Conditions = types.SetCondition(engineImage.Status.Conditions, longhorn.EngineImageConditionTypeReady, longhorn.ConditionStatusFalse,
			longhorn.EngineImageConditionTypeReadyReasonDaemonSet, fmt.Sprintf("Engine image is not fully deployed on all nodes: %v of %v", deployedNodeCount, len(engineImage.Status.NodeDeploymentMap)))
		engineImage.Status.State = longhorn.EngineImageStateDeploying
	} else {
		engineImage.Status.Conditions = types.SetConditionAndRecord(engineImage.Status.Conditions,
			longhorn.EngineImageConditionTypeReady, longhorn.ConditionStatusTrue,
			"", fmt.Sprintf("Engine image %v (%v) is fully deployed on all ready nodes", engineImage.Name, engineImage.Spec.Image),
			ic.eventRecorder, engineImage, corev1.EventTypeNormal)
		engineImage.Status.State = longhorn.EngineImageStateDeployed
	}

	if err := ic.handleAutoUpgradeEngineImageToDefaultEngineImage(engineImage.Spec.Image); err != nil {
		log.WithError(err).Warn("error when handleAutoUpgradeEngineImageToDefaultEngineImage")
	}

	return nil
}

func (ic *EngineImageController) syncNodeDeploymentMap(engineImage *longhorn.EngineImage) (err error) {
	defer func() {
		err = errors.Wrapf(err, "cannot sync NodeDeploymenMap for engine image %v", engineImage.Name)
	}()

	// initialize deployment map for all known nodes
	nodeDeploymentMap, err := func() (map[string]bool, error) {
		deployed := map[string]bool{}
		nodes, err := ic.ds.ListNodesRO()
		for _, node := range nodes {
			deployed[node.Name] = false
		}
		return deployed, err
	}()
	if err != nil {
		return err
	}

	eiDaemonSetPods, err := ic.ds.ListEngineImageDaemonSetPodsFromEngineImageNameRO(engineImage.Name)
	if err != nil {
		return err
	}
	for _, pod := range eiDaemonSetPods {
		allContainerReady := true
		for _, containerStatus := range pod.Status.ContainerStatuses {
			allContainerReady = allContainerReady && containerStatus.Ready
		}
		nodeDeploymentMap[pod.Spec.NodeName] = allContainerReady
	}

	engineImage.Status.NodeDeploymentMap = nodeDeploymentMap

	return nil
}

// handleAutoUpgradeEngineImageToDefaultEngineImage automatically upgrades volume's engine image to default engine image when it is applicable
func (ic *EngineImageController) handleAutoUpgradeEngineImageToDefaultEngineImage(currentProcessingImage string) error {
	defaultEngineImage, err := ic.ds.GetSettingValueExisted(types.SettingNameDefaultEngineImage)
	if err != nil {
		return err
	}

	// To avoid multiple managers doing upgrade at the same time, only allow the
	// manager that is responsible for the default engine image to do the upgrade
	if currentProcessingImage != defaultEngineImage {
		return nil
	}

	defaultEngineImageResource, err := ic.ds.GetEngineImage(types.GetEngineImageChecksumName(defaultEngineImage))
	if err != nil {
		return err
	}

	concurrentAutomaticEngineUpgradePerNodeLimit, err := ic.ds.GetSettingAsInt(types.SettingNameConcurrentAutomaticEngineUpgradePerNodeLimit)
	if err != nil {
		return err
	}
	if concurrentAutomaticEngineUpgradePerNodeLimit <= 0 {
		return nil
	}

	// List all volumes and select a set of volume for upgrading.
	volumes, err := ic.ds.ListVolumes()
	if err != nil {
		return err
	}

	candidates, inProgress := ic.getVolumesForEngineImageUpgrading(volumes, defaultEngineImageResource)

	limitedCandidates := limitAutomaticEngineUpgradePerNode(candidates, inProgress, int(concurrentAutomaticEngineUpgradePerNodeLimit))

	for _, vs := range limitedCandidates {
		for _, v := range vs {
			ic.logger.WithFields(logrus.Fields{"volume": v.Name, "image": v.Spec.Image}).Infof("Upgrading volume engine image to the default engine image %v automatically", defaultEngineImage)

			if types.IsDataEngineV2(v.Spec.DataEngine) {
				ic.logger.WithFields(logrus.Fields{"volume": v.Name, "image": v.Spec.Image}).Infof("Skip upgrading volume engine image to the default engine image %v automatically since it is using v2 data engine", defaultEngineImage)
				continue
			}

			v.Spec.Image = defaultEngineImage
			_, err = ic.ds.UpdateVolume(v)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func limitAutomaticEngineUpgradePerNode(candidates, inProgress map[string][]*longhorn.Volume, maxLimit int) (limitedCandidates map[string][]*longhorn.Volume) {
	limitedCandidates = make(map[string][]*longhorn.Volume)
	for node := range candidates {
		currentUpgrading := len(inProgress[node])
		if currentUpgrading >= maxLimit {
			continue
		}
		upperBound := util.MinInt(maxLimit-currentUpgrading, len(candidates[node]))
		limitedCandidates[node] = candidates[node][:upperBound]
	}
	return limitedCandidates
}

// getVolumesForEngineImageUpgrading returns 2 maps: map of volumes that are qualified for engine image upgrading
// and map of volumes that are upgrading engine image
// A volume is qualified for engine image upgrading if it meets one of the following case:
// Case 1:
//  1. Volume is in detached state
//  2. newEngineImageResource is deployed on the all volume's replicas' nodes
//
// Case 2:
//  1. Volume is not in engine upgrading process
//  2. newEngineImageResource is deployed on attaching node and the all volume's replicas' nodes
//  3. Volume is in attached state and it is able to do live upgrade
func (ic *EngineImageController) getVolumesForEngineImageUpgrading(volumes map[string]*longhorn.Volume, newEngineImageResource *longhorn.EngineImage) (candidates, inProgress map[string][]*longhorn.Volume) {
	candidates = make(map[string][]*longhorn.Volume)
	inProgress = make(map[string][]*longhorn.Volume)

	for _, v := range volumes {
		if v.Spec.Image != v.Status.CurrentImage {
			inProgress[v.Status.OwnerID] = append(inProgress[v.Status.OwnerID], v)
			continue
		}
		canBeUpgraded := ic.canDoOfflineEngineImageUpgrade(v, newEngineImageResource) || ic.canDoLiveEngineImageUpgrade(v, newEngineImageResource)
		isCurrentEIAvailable, _ := ic.ds.CheckImageReadyOnAllVolumeReplicas(v.Status.CurrentImage, v.Name, v.Status.CurrentNodeID, v.Spec.DataEngine)
		isNewEIAvailable, _ := ic.ds.CheckImageReadyOnAllVolumeReplicas(newEngineImageResource.Spec.Image, v.Name, v.Status.CurrentNodeID, v.Spec.DataEngine)
		validCandidate := v.Spec.Image != newEngineImageResource.Spec.Image && canBeUpgraded && isCurrentEIAvailable && isNewEIAvailable
		if validCandidate {
			candidates[v.Status.OwnerID] = append(candidates[v.Status.OwnerID], v)
		}
	}

	return candidates, inProgress
}

func (ic *EngineImageController) canDoOfflineEngineImageUpgrade(v *longhorn.Volume, newEngineImageResource *longhorn.EngineImage) bool {
	return v.Status.State == longhorn.VolumeStateDetached
}

// canDoLiveEngineImageUpgrade check if it is possible to do live engine upgrade for a volume
// A volume can do live engine upgrade when:
//  1. Volume is attached AND
//  2. Volume's robustness is healthy AND
//  3. Volume is not a DR volume AND
//  4. Volume is not expanding AND
//  5. Volume is not migrating AND
//  6. Volume is not strict-local AND
//  7. The current volume's engine image is compatible with the new engine image
func (ic *EngineImageController) canDoLiveEngineImageUpgrade(v *longhorn.Volume, newEngineImageResource *longhorn.EngineImage) bool {
	if v.Status.State != longhorn.VolumeStateAttached {
		return false
	}
	if v.Status.Robustness != longhorn.VolumeRobustnessHealthy {
		return false
	}
	if v.Status.IsStandby {
		return false
	}
	if v.Status.ExpansionRequired {
		return false
	}
	if util.IsVolumeMigrating(v) {
		return false
	}
	if v.Spec.DataLocality == longhorn.DataLocalityStrictLocal {
		return false
	}
	oldEngineImageResource, err := ic.ds.GetEngineImage(types.GetEngineImageChecksumName(v.Status.CurrentImage))
	if err != nil {
		return false
	}
	if oldEngineImageResource.Status.ControllerAPIVersion > newEngineImageResource.Status.ControllerAPIVersion ||
		oldEngineImageResource.Status.ControllerAPIVersion < newEngineImageResource.Status.ControllerAPIMinVersion {
		return false
	}
	return true
}

func updateEngineImageVersion(ei *longhorn.EngineImage) error {
	engineCollection := &engineapi.EngineCollection{}
	// we're getting local longhorn engine version, don't need volume etc
	client, err := engineCollection.NewEngineClient(&engineapi.EngineClientRequest{
		EngineImage: ei.Spec.Image,
		VolumeName:  "",
		IP:          "",
		Port:        0,
	})
	if err != nil {
		return errors.Wrapf(err, "cannot get engine client to check engine version")
	}
	version, err := client.VersionGet(nil, true)
	if err != nil {
		return errors.Wrapf(err, "cannot get engine version for %v (%v)", ei.Name, ei.Spec.Image)
	}

	ei.Status.EngineVersionDetails = *version.ClientVersion
	return nil
}

func (ic *EngineImageController) countVolumesUsingEngineImage(image string) (int, error) {
	volumes, err := ic.ds.ListVolumesRO()
	if err != nil {
		return 0, err
	}

	count := 0
	for _, v := range volumes {
		if v.Spec.Image == image || v.Status.CurrentImage == image {
			count++
		}
	}
	return count, nil
}

func (ic *EngineImageController) countEnginesUsingEngineImage(image string) (int, error) {
	engines, err := ic.ds.ListEnginesRO()
	if err != nil {
		return 0, err
	}

	count := 0
	for _, e := range engines {
		if e.Spec.Image == image || e.Status.CurrentImage == image {
			count++
		}
	}
	return count, nil
}

func (ic *EngineImageController) countReplicasUsingEngineImage(image string) (int, error) {
	replicas, err := ic.ds.ListReplicasRO()
	if err != nil {
		return 0, err
	}

	count := 0
	for _, r := range replicas {
		if r.Spec.Image == image || r.Status.CurrentImage == image {
			count++
		}
	}
	return count, nil
}

func (ic *EngineImageController) countCRsUsingEngineImage(ei *longhorn.EngineImage) (int, error) {
	refCount := 0

	count, err := ic.countVolumesUsingEngineImage(ei.Spec.Image)
	if err != nil {
		return 0, errors.Wrapf(err, "failed to count volumes using engine image %v", ei.Spec.Image)
	}
	refCount += count

	count, err = ic.countEnginesUsingEngineImage(ei.Spec.Image)
	if err != nil {
		return 0, errors.Wrapf(err, "failed to count engines using engine image %v", ei.Spec.Image)
	}
	refCount += count

	count, err = ic.countReplicasUsingEngineImage(ei.Spec.Image)
	if err != nil {
		return 0, errors.Wrapf(err, "failed to count replicas using engine image %v", ei.Spec.Image)
	}
	refCount += count

	return refCount, nil
}

func (ic *EngineImageController) updateEngineImageRefCount(ei *longhorn.EngineImage) error {
	refCount, err := ic.countCRsUsingEngineImage(ei)
	if err != nil {
		return errors.Wrapf(err, "failed to count CRs using engine image %v", ei.Spec.Image)
	}

	ei.Status.RefCount = refCount
	if ei.Status.RefCount == 0 {
		if ei.Status.NoRefSince == "" {
			ei.Status.NoRefSince = ic.nowHandler()
		}
	} else {
		ei.Status.NoRefSince = ""
	}
	return nil

}

func (ic *EngineImageController) cleanupExpiredEngineImage(ei *longhorn.EngineImage) (err error) {
	defer func() {
		err = errors.Wrapf(err, "cannot cleanup engine image %v (%v)", ei.Name, ei.Spec.Image)
	}()

	if ei.Status.RefCount != 0 {
		return nil
	}
	if ei.Status.NoRefSince == "" {
		return nil
	}
	if util.TimestampAfterTimeout(ei.Status.NoRefSince, ExpiredEngineImageTimeout) {
		defaultEngineImageValue, err := ic.ds.GetSettingValueExisted(types.SettingNameDefaultEngineImage)
		if err != nil {
			return err
		}
		// Don't delete the default image
		if ei.Spec.Image == defaultEngineImageValue {
			return nil
		}

		log := getLoggerForEngineImage(ic.logger, ei)
		log.Info("Cleaning engine image since it expired")
		// TODO: Need to consider if the engine image can be removed in engine image controller
		if err := ic.ds.DeleteEngineImage(ei.Name); err != nil {
			return err
		}
		return nil
	}
	return nil
}

func (ic *EngineImageController) enqueueEngineImage(obj interface{}) {
	key, err := controller.KeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %v", obj, err))
		return
	}

	ic.queue.Add(key)
}

func (ic *EngineImageController) enqueueVolumes(volumes ...interface{}) {
	images := map[string]struct{}{}
	for _, obj := range volumes {
		v, isVolume := obj.(*longhorn.Volume)
		if !isVolume {
			deletedState, ok := obj.(cache.DeletedFinalStateUnknown)
			if !ok {
				utilruntime.HandleError(fmt.Errorf("received unexpected obj: %#v", obj))
				continue
			}

			// use the last known state, to enqueue, dependent objects
			v, ok = deletedState.Obj.(*longhorn.Volume)
			if !ok {
				utilruntime.HandleError(fmt.Errorf("DeletedFinalStateUnknown contained invalid object: %#v", deletedState.Obj))
				continue
			}
		}

		if _, ok := images[v.Spec.Image]; !ok {
			images[v.Spec.Image] = struct{}{}
		}
		if v.Status.CurrentImage != "" {
			if _, ok := images[v.Status.CurrentImage]; !ok {
				images[v.Status.CurrentImage] = struct{}{}
			}
		}
	}

	for img := range images {
		engineImage, err := ic.ds.GetEngineImage(types.GetEngineImageChecksumName(img))
		if err != nil {
			continue
		}
		ic.enqueueEngineImage(engineImage)
	}
}

func (ic *EngineImageController) enqueueControlleeChange(obj interface{}) {
	if deletedState, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = deletedState.Obj
	}

	metaObj, err := meta.Accessor(obj)

	if err != nil {
		ic.logger.WithError(err).Warnf("Failed to convert obj %v to metav1.Object", obj)
		return
	}
	ownerRefs := metaObj.GetOwnerReferences()
	for _, ref := range ownerRefs {
		namespace := metaObj.GetNamespace()
		ic.ResolveRefAndEnqueue(namespace, &ref)
		return
	}
}

func (ic *EngineImageController) ResolveRefAndEnqueue(namespace string, ref *metav1.OwnerReference) {
	if ref.Kind != types.LonghornKindEngineImage {
		// TODO: Will stop checking this wrong reference kind after all Longhorn components having used the new kinds
		if ref.Kind != ownerKindEngineImage {
			return
		}
	}
	engineImage, err := ic.ds.GetEngineImage(ref.Name)
	if err != nil {
		return
	}
	if engineImage.UID != ref.UID {
		// The controller we found with this Name is not the same one that the
		// OwnerRef points to.
		return
	}
	ic.enqueueEngineImage(engineImage)
}

func (ic *EngineImageController) createEngineImageDaemonSetSpec(ei *longhorn.EngineImage, tolerations []corev1.Toleration,
	priorityClass, registrySecret string, imagePullPolicy corev1.PullPolicy, nodeSelector map[string]string) (*appsv1.DaemonSet, error) {

	dsName := types.GetDaemonSetNameFromEngineImageName(ei.Name)
	image := ei.Spec.Image
	cmd := []string{
		"/bin/bash",
	}
	args := []string{
		"-c",
		"diff /usr/local/bin/longhorn /data/longhorn > /dev/null 2>&1; " +
			"if [ $? -ne 0 ]; then cp -p /usr/local/bin/longhorn /data/ && echo installed; fi && " +
			"trap 'rm /data/longhorn* && echo cleaned up' EXIT && sleep infinity",
	}
	maxUnavailable := intstr.FromString(`100%`)
	privileged := true
	tolerationsByte, err := json.Marshal(tolerations)
	if err != nil {
		return nil, err
	}

	d := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        dsName,
			Annotations: map[string]string{types.GetLonghornLabelKey(types.LastAppliedTolerationAnnotationKeySuffix): string(tolerationsByte)},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: types.GetEIDaemonSetLabelSelector(ei.Name),
			},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type: appsv1.RollingUpdateDaemonSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDaemonSet{
					MaxUnavailable: &maxUnavailable,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:            dsName,
					Labels:          types.GetEIDaemonSetLabelSelector(ei.Name),
					OwnerReferences: datastore.GetOwnerReferencesForEngineImage(ei),
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: ic.serviceAccount,
					Tolerations:        tolerations,
					NodeSelector:       nodeSelector,
					PriorityClassName:  priorityClass,
					Containers: []corev1.Container{
						{
							Name:            dsName,
							Image:           image,
							Command:         cmd,
							Args:            args,
							ImagePullPolicy: imagePullPolicy,
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "data",
									MountPath: "/data/",
								},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									Exec: &corev1.ExecAction{
										Command: []string{
											"sh", "-c",
											"ls /data/longhorn && /data/longhorn version --client-only",
										},
									},
								},
								InitialDelaySeconds: datastore.PodProbeInitialDelay,
								TimeoutSeconds:      datastore.PodProbeTimeoutSeconds,
								PeriodSeconds:       datastore.PodProbePeriodSeconds,
								FailureThreshold:    datastore.PodLivenessProbeFailureThreshold,
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									Exec: &corev1.ExecAction{
										Command: []string{
											"sh", "-c",
											"/data/longhorn version --client-only",
										},
									},
								},
								InitialDelaySeconds: datastore.PodProbeInitialDelay,
								TimeoutSeconds:      datastore.PodProbeTimeoutSeconds,
								PeriodSeconds:       datastore.PodProbePeriodSeconds,
								FailureThreshold:    datastore.PodLivenessProbeFailureThreshold,
							},
							SecurityContext: &corev1.SecurityContext{
								Privileged: &privileged,
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "data",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: types.GetEngineBinaryDirectoryOnHostForImage(image),
								},
							},
						},
					},
				},
			},
		},
	}

	if registrySecret != "" {
		d.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{
			{
				Name: registrySecret,
			},
		}
	}
	types.AddGoCoverDirToDaemonSet(d)

	return d, nil
}

func (ic *EngineImageController) isResponsibleFor(ei *longhorn.EngineImage) (bool, error) {
	var err error
	defer func() {
		err = errors.Wrap(err, "error while checking isResponsibleFor")
	}()

	readyNodesWithDefaultEI, err := ic.ds.ListReadyNodesContainingEngineImageRO(ei.Spec.Image)
	if err != nil {
		return false, err
	}
	isResponsible := isControllerResponsibleFor(ic.controllerID, ic.ds, ei.Name, "", ei.Status.OwnerID)

	if len(readyNodesWithDefaultEI) == 0 {
		return isResponsible, nil
	}

	currentOwnerEngineAvailable, err := ic.ds.CheckEngineImageReadiness(ei.Spec.Image, ei.Status.OwnerID)
	if err != nil {
		return false, err
	}
	currentNodeEngineAvailable, err := ic.ds.CheckEngineImageReadiness(ei.Spec.Image, ic.controllerID)
	if err != nil {
		return false, err
	}

	isPreferredOwner := currentNodeEngineAvailable && isResponsible
	continueToBeOwner := currentNodeEngineAvailable && ic.controllerID == ei.Status.OwnerID
	requiresNewOwner := currentNodeEngineAvailable && !currentOwnerEngineAvailable
	return isPreferredOwner || continueToBeOwner || requiresNewOwner, nil
}
