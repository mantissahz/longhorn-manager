package util

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/mod/semver"

	"github.com/jinzhu/copier"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/record"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"

	emeta "github.com/longhorn/longhorn-engine/pkg/meta"

	"github.com/longhorn/longhorn-manager/constant"
	"github.com/longhorn/longhorn-manager/meta"
	"github.com/longhorn/longhorn-manager/types"

	longhorn "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta2"
	lhclientset "github.com/longhorn/longhorn-manager/k8s/pkg/client/clientset/versioned"
)

const (
	// LonghornV1ToV2MinorVersionNum v1 minimal minor version when the upgrade path is from v1.x to v2.0
	// TODO: decide the v1 minimum version that could be upgraded to v2.0
	LonghornV1ToV2MinorVersionNum = 30

	skipUpgradeVersionCheckMsg = "It is not recommended to disable the upgrade version check from %s to %s"
)

type ProgressMonitor struct {
	description                 string
	targetValue                 int
	currentValue                int
	currentProgressInPercentage float64
	mutex                       *sync.RWMutex
}

func NewProgressMonitor(description string, currentValue, targetValue int) *ProgressMonitor {
	pm := &ProgressMonitor{
		description:                 description,
		targetValue:                 targetValue,
		currentValue:                currentValue,
		currentProgressInPercentage: math.Floor(float64(currentValue*100) / float64(targetValue)),
		mutex:                       &sync.RWMutex{},
	}
	pm.logCurrentProgress()
	return pm
}

func (pm *ProgressMonitor) logCurrentProgress() {
	logrus.Infof("%v: current progress %v%% (%v/%v)", pm.description, pm.currentProgressInPercentage, pm.currentValue, pm.targetValue)
}

func (pm *ProgressMonitor) Inc() int {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()

	oldProgressInPercentage := pm.currentProgressInPercentage

	pm.currentValue++
	pm.currentProgressInPercentage = math.Floor(float64(pm.currentValue*100) / float64(pm.targetValue))
	if pm.currentProgressInPercentage != oldProgressInPercentage {
		pm.logCurrentProgress()
	}
	return pm.currentValue
}

func (pm *ProgressMonitor) SetCurrentValue(newValue int) {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()

	oldProgressInPercentage := pm.currentProgressInPercentage

	pm.currentValue = newValue
	pm.currentProgressInPercentage = math.Floor(float64(pm.currentValue*100) / float64(pm.targetValue))
	if pm.currentProgressInPercentage != oldProgressInPercentage {
		pm.logCurrentProgress()
	}
}

func (pm *ProgressMonitor) GetCurrentProgress() (int, int, float64) {
	pm.mutex.RLock()
	defer pm.mutex.RUnlock()
	return pm.currentValue, pm.targetValue, pm.currentProgressInPercentage
}

func ListShareManagerPods(namespace string, kubeClient *clientset.Clientset) ([]corev1.Pod, error) {
	smPodsList, err := kubeClient.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: labels.Set(types.GetShareManagerComponentLabel()).String(),
	})
	if err != nil {
		return nil, err
	}
	return smPodsList.Items, nil
}

func ListIMPods(namespace string, kubeClient *clientset.Clientset) ([]corev1.Pod, error) {
	imPodsList, err := kubeClient.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", types.GetLonghornLabelComponentKey(), types.LonghornLabelInstanceManager),
	})
	if err != nil {
		return nil, err
	}
	return imPodsList.Items, nil
}

func ListManagerPods(namespace string, kubeClient *clientset.Clientset) ([]corev1.Pod, error) {
	managerPodsList, err := kubeClient.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: labels.Set(types.GetManagerLabels()).String(),
	})
	if err != nil {
		return nil, err
	}
	return managerPodsList.Items, nil
}

func GetCurrentLonghornVersion(namespace string, lhClient lhclientset.Interface) (string, error) {
	currentLHVersionSetting, err := lhClient.LonghornV1beta2().Settings(namespace).Get(context.TODO(), string(types.SettingNameCurrentLonghornVersion), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}

	return currentLHVersionSetting.Value, nil
}

func GetCurrentReferredLonghornEngineImageVersions(namespace string, lhClient lhclientset.Interface) (map[string]emeta.VersionOutput, error) {
	engineImages, err := lhClient.LonghornV1beta2().EngineImages(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	engineImageVersionMap := map[string]emeta.VersionOutput{}
	for _, engineImage := range engineImages.Items {
		if engineImage.Status.RefCount == 0 {
			continue
		}

		engineImageVersionMap[engineImage.Name] = emeta.VersionOutput{
			ControllerAPIVersion: engineImage.Status.ControllerAPIVersion,
			CLIAPIVersion:        engineImage.Status.CLIAPIVersion,
		}
	}

	return engineImageVersionMap, nil
}

func CreateOrUpdateLonghornVersionSetting(namespace string, lhClient *lhclientset.Clientset) error {
	s, err := lhClient.LonghornV1beta2().Settings(namespace).Get(context.TODO(), string(types.SettingNameCurrentLonghornVersion), metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}

		s = &longhorn.Setting{
			ObjectMeta: metav1.ObjectMeta{
				Name: string(types.SettingNameCurrentLonghornVersion),
			},
			Value: meta.Version,
		}
		_, err := lhClient.LonghornV1beta2().Settings(namespace).Create(context.TODO(), s, metav1.CreateOptions{})
		return err
	}

	if s.Value != meta.Version {
		s.Value = meta.Version
		if s.Annotations == nil {
			s.Annotations = make(map[string]string)
		}
		s.Annotations[types.GetLonghornLabelKey(types.UpdateSettingFromLonghorn)] = ""
		s, err = lhClient.LonghornV1beta2().Settings(namespace).Update(context.TODO(), s, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict})
		if err != nil {
			return err
		}
		delete(s.Annotations, types.GetLonghornLabelKey(types.UpdateSettingFromLonghorn))
		_, err = lhClient.LonghornV1beta2().Settings(namespace).Update(context.TODO(), s, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict})
		return err
	}
	return nil
}

func CheckUpgradePath(namespace string, lhClient lhclientset.Interface, eventRecorder record.EventRecorder, enableUpgradeVersionCheck bool) error {
	lhCurrentVersion, err := GetCurrentLonghornVersion(namespace, lhClient)
	if err != nil {
		return err
	}
	if !enableUpgradeVersionCheck && lhCurrentVersion != "" {
		if semver.Compare(lhCurrentVersion, "v1.4.0") < 0 {
			return fmt.Errorf("failed to skip the upgrade check for %s to upgrade to %s, as it only supports the current version larger than v1.4.0", lhCurrentVersion, meta.Version)
		}
		logrus.Warnf(skipUpgradeVersionCheckMsg, lhCurrentVersion, meta.Version)
		eventRecorder.Eventf(&corev1.ObjectReference{Namespace: namespace, Name: "longhorn-upgrade"}, corev1.EventTypeWarning, constant.EventReasonUpgrade, skipUpgradeVersionCheckMsg, lhCurrentVersion, meta.Version)
		return nil
	}

	if err := checkLHUpgradePath(namespace, lhClient); err != nil {
		return err
	}

	return checkEngineUpgradePath(namespace, lhClient, emeta.GetVersion())
}

// checkLHUpgradePath returns if the upgrade path from lhCurrentVersion to meta.Version is supported.
//
//	For example: upgrade path is from x.y.z to a.b.c,
//	0 <= a-x <= 1 is supported, and y should be after a specific version if a-x == 1
//	0 <= b-y <= 1 is supported when a-x == 0
//	all downgrade is not supported
func checkLHUpgradePath(namespace string, lhClient lhclientset.Interface) error {
	lhCurrentVersion, err := GetCurrentLonghornVersion(namespace, lhClient)
	if err != nil {
		return err
	}

	if lhCurrentVersion == "" {
		return nil
	}

	logrus.Infof("Checking if the upgrade path from %v to %v is supported", lhCurrentVersion, meta.Version)

	if !semver.IsValid(meta.Version) {
		return fmt.Errorf("failed to upgrade since upgrading version %v is not valid", meta.Version)
	}

	lhNewMajorVersion := semver.Major(meta.Version)
	lhCurrentMajorVersion := semver.Major(lhCurrentVersion)

	lhNewMajorVersionNum, lhNewMinorVersionNum, err := getMajorMinorInt(meta.Version)
	if err != nil {
		return errors.Wrapf(err, "failed to parse upgrading %v major/minor version", meta.Version)
	}

	lhCurrentMajorVersionNum, lhCurrentMinorVersionNum, err := getMajorMinorInt(lhCurrentVersion)
	if err != nil {
		return errors.Wrapf(err, "failed to parse current %v major/minor version", meta.Version)
	}

	if semver.Compare(lhCurrentMajorVersion, lhNewMajorVersion) > 0 {
		return fmt.Errorf("failed to upgrade since downgrading from %v to %v for major version is not supported", lhCurrentVersion, meta.Version)
	}

	if semver.Compare(lhCurrentMajorVersion, lhNewMajorVersion) < 0 {
		if (lhNewMajorVersionNum - lhCurrentMajorVersionNum) > 1 {
			return fmt.Errorf("failed to upgrade since upgrading from %v to %v for major version is not supported", lhCurrentVersion, meta.Version)
		}
		if lhCurrentMinorVersionNum < LonghornV1ToV2MinorVersionNum {
			return fmt.Errorf("failed to upgrade since upgrading major version with minor version under %v is not supported", LonghornV1ToV2MinorVersionNum)
		}
		return nil
	}

	if (lhNewMinorVersionNum - lhCurrentMinorVersionNum) > 1 {
		return fmt.Errorf("failed to upgrade since upgrading from %v to %v for minor version is not supported", lhCurrentVersion, meta.Version)
	}

	if (lhNewMinorVersionNum - lhCurrentMinorVersionNum) == 1 {
		return nil
	}

	if semver.Compare(lhCurrentVersion, meta.Version) > 0 {
		return fmt.Errorf("failed to upgrade since downgrading from %v to %v is not supported", lhCurrentVersion, meta.Version)
	}

	return nil
}

// checkEngineUpgradePath returns error if the upgrade path from
// the current EngineImage version to emeta.ControllerAPIVersion is not supported.
func checkEngineUpgradePath(namespace string, lhClient lhclientset.Interface, newEngineVersion emeta.VersionOutput) (err error) {
	defer func() {
		err = errors.Wrapf(err, "failed checking Engine upgrade path")
	}()

	engineImageVersions, err := GetCurrentReferredLonghornEngineImageVersions(namespace, lhClient)
	if err != nil {
		return err
	}

	if len(engineImageVersions) == 0 {
		return nil
	}

	logrus.WithFields(logrus.Fields{
		"newEngineControllerAPIVersion":    newEngineVersion.ControllerAPIVersion,
		"newEngineControllerAPIMinVersion": newEngineVersion.ControllerAPIMinVersion,
		"newEngineClientAPIVersion":        newEngineVersion.CLIAPIVersion,
		"newEngineClientAPIMinVersion":     newEngineVersion.CLIAPIMinVersion,
	}).Infof("Checking if the engine upgrade path from %+v is supported", engineImageVersions)

	checkSupportVersion := func(currentVersion, newVersion, newMinVersion int) error {
		if currentVersion > newVersion {
			return fmt.Errorf("downgrading from %v to %v is not supported", currentVersion, newVersion)
		}

		if currentVersion < newMinVersion {
			return fmt.Errorf("found version %v is below required minimal version %v", currentVersion, newMinVersion)
		}

		return nil
	}

	for name, engineImageVersion := range engineImageVersions {
		err = checkSupportVersion(engineImageVersion.ControllerAPIVersion, newEngineVersion.ControllerAPIVersion, newEngineVersion.ControllerAPIMinVersion)
		if err != nil {
			return errors.Wrapf(err, "incompatible Engine %v controller API version", name)
		}

		err = checkSupportVersion(engineImageVersion.CLIAPIVersion, newEngineVersion.CLIAPIVersion, newEngineVersion.CLIAPIMinVersion)
		if err != nil {
			return errors.Wrapf(err, "incompatible Engine %v client API version", name)
		}
	}
	return nil
}

func DeleteRemovedSettings(namespace string, lhClient *lhclientset.Clientset) error {
	isKnownSetting := func(knownSettingNames []types.SettingName, name types.SettingName) bool {
		for _, knownSettingName := range knownSettingNames {
			if name == knownSettingName {
				return true
			}
		}
		// replaced settings will be deleted after replaced
		return types.IsSettingReplaced(name)
	}

	existingSettingList, err := lhClient.LonghornV1beta2().Settings(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, existingSetting := range existingSettingList.Items {
		if !isKnownSetting(types.SettingNameList, types.SettingName(existingSetting.Name)) {
			logrus.Infof("Deleting removed setting: %v", existingSetting.Name)
			if err = lhClient.LonghornV1beta2().Settings(namespace).Delete(context.TODO(), existingSetting.Name, metav1.DeleteOptions{}); err != nil {
				return err
			}
		}
	}

	return nil
}

func getMajorMinorInt(v string) (int, int, error) {
	majorNum, err := getMajorInt(v)
	if err != nil {
		return -1, -1, err
	}
	minorNum, err := getMinorInt(v)
	if err != nil {
		return -1, -1, err
	}
	return majorNum, minorNum, nil
}

func getMajorInt(v string) (int, error) {
	versionStr := removeFirstChar(v)
	return strconv.Atoi(strings.Split(versionStr, ".")[0])
}

func getMinorInt(v string) (int, error) {
	versionStr := removeFirstChar(v)
	return strconv.Atoi(strings.Split(versionStr, ".")[1])
}

func removeFirstChar(v string) string {
	if v[0] == 'v' {
		return v[1:]
	}
	return v
}

// ListAndUpdateSettingsInProvidedCache list all settings and save them into the provided cached `resourceMap`. This method is not thread-safe.
func ListAndUpdateSettingsInProvidedCache(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}) (map[string]*longhorn.Setting, error) {
	if v, ok := resourceMaps[types.LonghornKindSetting]; ok {
		return v.(map[string]*longhorn.Setting), nil
	}

	settings := map[string]*longhorn.Setting{}
	settingList, err := lhClient.LonghornV1beta2().Settings(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, setting := range settingList.Items {
		if _, exist := types.GetSettingDefinition(types.SettingName(setting.Name)); !exist {
			logrus.Warnf("Found unknown setting %v, skipping", setting.Name)
			continue
		}
		settingCopy := longhorn.Setting{}
		if err := copier.Copy(&settingCopy, setting); err != nil {
			return nil, err
		}
		settings[setting.Name] = &settingCopy
	}

	resourceMaps[types.LonghornKindSetting] = settings

	return settings, nil
}

// ListAndUpdateNodesInProvidedCache list all nodes and save them into the provided cached `resourceMap`. This method is not thread-safe.
func ListAndUpdateNodesInProvidedCache(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}) (map[string]*longhorn.Node, error) {
	if v, ok := resourceMaps[types.LonghornKindNode]; ok {
		return v.(map[string]*longhorn.Node), nil
	}

	nodes := map[string]*longhorn.Node{}
	nodeList, err := lhClient.LonghornV1beta2().Nodes(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i, node := range nodeList.Items {
		nodes[node.Name] = &nodeList.Items[i]
	}

	resourceMaps[types.LonghornKindNode] = nodes

	return nodes, nil
}

// ListAndUpdateOrphansInProvidedCache list all orphans and save them into the provided cached `resourceMap`. This method is not thread-safe.
func ListAndUpdateOrphansInProvidedCache(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}) (map[string]*longhorn.Orphan, error) {
	if v, ok := resourceMaps[types.LonghornKindOrphan]; ok {
		return v.(map[string]*longhorn.Orphan), nil
	}

	orphans := map[string]*longhorn.Orphan{}
	orphanList, err := lhClient.LonghornV1beta2().Orphans(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i, orphan := range orphanList.Items {
		orphans[orphan.Name] = &orphanList.Items[i]
	}

	resourceMaps[types.LonghornKindOrphan] = orphans

	return orphans, nil
}

// ListAndUpdateInstanceManagersInProvidedCache list all instanceManagers and save them into the provided cached `resourceMap`. This method is not thread-safe.
func ListAndUpdateInstanceManagersInProvidedCache(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}) (map[string]*longhorn.InstanceManager, error) {
	if v, ok := resourceMaps[types.LonghornKindInstanceManager]; ok {
		return v.(map[string]*longhorn.InstanceManager), nil
	}

	ims := map[string]*longhorn.InstanceManager{}
	imList, err := lhClient.LonghornV1beta2().InstanceManagers(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i, im := range imList.Items {
		ims[im.Name] = &imList.Items[i]
	}

	resourceMaps[types.LonghornKindInstanceManager] = ims

	return ims, nil
}

// ListAndUpdateVolumesInProvidedCache list all volumes and save them into the provided cached `resourceMap`. This method is not thread-safe.
func ListAndUpdateVolumesInProvidedCache(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}) (map[string]*longhorn.Volume, error) {
	if v, ok := resourceMaps[types.LonghornKindVolume]; ok {
		return v.(map[string]*longhorn.Volume), nil
	}

	volumes := map[string]*longhorn.Volume{}
	volumeList, err := lhClient.LonghornV1beta2().Volumes(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i, volume := range volumeList.Items {
		volumes[volume.Name] = &volumeList.Items[i]
	}

	resourceMaps[types.LonghornKindVolume] = volumes

	return volumes, nil
}

// ListAndUpdateReplicasInProvidedCache list all replicas and save them into the provided cached `resourceMap`. This method is not thread-safe.
func ListAndUpdateReplicasInProvidedCache(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}) (map[string]*longhorn.Replica, error) {
	if v, ok := resourceMaps[types.LonghornKindReplica]; ok {
		return v.(map[string]*longhorn.Replica), nil
	}

	replicas := map[string]*longhorn.Replica{}
	replicaList, err := lhClient.LonghornV1beta2().Replicas(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i, replica := range replicaList.Items {
		replicas[replica.Name] = &replicaList.Items[i]
	}

	resourceMaps[types.LonghornKindReplica] = replicas

	return replicas, nil
}

// ListAndUpdateEnginesInProvidedCache list all engines and save them into the provided cached `resourceMap`. This method is not thread-safe.
func ListAndUpdateEnginesInProvidedCache(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}) (map[string]*longhorn.Engine, error) {
	if v, ok := resourceMaps[types.LonghornKindEngine]; ok {
		return v.(map[string]*longhorn.Engine), nil
	}

	engines := map[string]*longhorn.Engine{}
	engineList, err := lhClient.LonghornV1beta2().Engines(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i, engine := range engineList.Items {
		engines[engine.Name] = &engineList.Items[i]
	}

	resourceMaps[types.LonghornKindEngine] = engines

	return engines, nil
}

// ListAndUpdateBackupTargetsInProvidedCache list all backup targets and save them into the provided cached `resourceMap`. This method is not thread-safe.
func ListAndUpdateBackupTargetsInProvidedCache(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}) (map[string]*longhorn.BackupTarget, error) {
	if v, ok := resourceMaps[types.LonghornKindBackupTarget]; ok {
		return v.(map[string]*longhorn.BackupTarget), nil
	}

	backupTargets := map[string]*longhorn.BackupTarget{}
	backupTargetList, err := lhClient.LonghornV1beta2().BackupTargets(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i, backupTarget := range backupTargetList.Items {
		backupTargets[backupTarget.Name] = &backupTargetList.Items[i]
	}

	resourceMaps[types.LonghornKindBackupTarget] = backupTargets

	return backupTargets, nil
}

// ListAndUpdateBackupVolumesInProvidedCache list all backup volumes and save them into the provided cached `resourceMap`. This method is not thread-safe.
func ListAndUpdateBackupVolumesInProvidedCache(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}) (map[string]*longhorn.BackupVolume, error) {
	if v, ok := resourceMaps[types.LonghornKindBackupVolume]; ok {
		return v.(map[string]*longhorn.BackupVolume), nil
	}

	backupVolumes := map[string]*longhorn.BackupVolume{}
	backupVolumeList, err := lhClient.LonghornV1beta2().BackupVolumes(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i, backupVolume := range backupVolumeList.Items {
		backupVolumes[backupVolume.Name] = &backupVolumeList.Items[i]
	}

	resourceMaps[types.LonghornKindBackupVolume] = backupVolumes

	return backupVolumes, nil
}

// ListAndUpdateBackupsInProvidedCache list all backups and save them into the provided cached `resourceMap`. This method is not thread-safe.
func ListAndUpdateBackupsInProvidedCache(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}) (map[string]*longhorn.Backup, error) {
	if v, ok := resourceMaps[types.LonghornKindBackup]; ok {
		return v.(map[string]*longhorn.Backup), nil
	}

	backups := map[string]*longhorn.Backup{}
	backupList, err := lhClient.LonghornV1beta2().Backups(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i, backup := range backupList.Items {
		backups[backup.Name] = &backupList.Items[i]
	}

	resourceMaps[types.LonghornKindBackup] = backups

	return backups, nil
}

// ListAndUpdateBackupBackingImagesInProvidedCache list all backupBackingImages and save them into the provided cached `resourceMap`. This method is not thread-safe.
func ListAndUpdateBackupBackingImagesInProvidedCache(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}) (map[string]*longhorn.BackupBackingImage, error) {
	if v, ok := resourceMaps[types.LonghornKindBackupBackingImage]; ok {
		return v.(map[string]*longhorn.BackupBackingImage), nil
	}

	bbis := map[string]*longhorn.BackupBackingImage{}
	bbisList, err := lhClient.LonghornV1beta2().BackupBackingImages(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i, bbi := range bbisList.Items {
		bbis[bbi.Name] = &bbisList.Items[i]
	}

	resourceMaps[types.LonghornKindBackupBackingImage] = bbis

	return bbis, nil
}

// ListAndUpdateSnapshotsInProvidedCache list all snapshots and save them into the provided cached `resourceMap`. This method is not thread-safe.
func ListAndUpdateSnapshotsInProvidedCache(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}) (map[string]*longhorn.Snapshot, error) {
	if v, ok := resourceMaps[types.LonghornKindSnapshot]; ok {
		return v.(map[string]*longhorn.Snapshot), nil
	}

	snapshots := map[string]*longhorn.Snapshot{}
	snapshotList, err := lhClient.LonghornV1beta2().Snapshots(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i, snapshot := range snapshotList.Items {
		snapshots[snapshot.Name] = &snapshotList.Items[i]
	}

	resourceMaps[types.LonghornKindSnapshot] = snapshots

	return snapshots, nil
}

// ListAndUpdateEngineImagesInProvidedCache list all engineImages and save them into the provided cached `resourceMap`. This method is not thread-safe.
func ListAndUpdateEngineImagesInProvidedCache(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}) (map[string]*longhorn.EngineImage, error) {
	if v, ok := resourceMaps[types.LonghornKindEngineImage]; ok {
		return v.(map[string]*longhorn.EngineImage), nil
	}

	eis := map[string]*longhorn.EngineImage{}
	eiList, err := lhClient.LonghornV1beta2().EngineImages(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i, ei := range eiList.Items {
		eis[ei.Name] = &eiList.Items[i]
	}

	resourceMaps[types.LonghornKindEngineImage] = eis

	return eis, nil
}

// ListAndUpdateShareManagersInProvidedCache list all shareManagers and save them into the provided cached `resourceMap`. This method is not thread-safe.
func ListAndUpdateShareManagersInProvidedCache(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}) (map[string]*longhorn.ShareManager, error) {
	if v, ok := resourceMaps[types.LonghornKindShareManager]; ok {
		return v.(map[string]*longhorn.ShareManager), nil
	}

	sms := map[string]*longhorn.ShareManager{}
	smList, err := lhClient.LonghornV1beta2().ShareManagers(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i, sm := range smList.Items {
		sms[sm.Name] = &smList.Items[i]
	}

	resourceMaps[types.LonghornKindShareManager] = sms

	return sms, nil
}

// ListAndUpdateBackingImagesInProvidedCache list all backingImages and save them into the provided cached `resourceMap`. This method is not thread-safe.
func ListAndUpdateBackingImagesInProvidedCache(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}) (map[string]*longhorn.BackingImage, error) {
	if v, ok := resourceMaps[types.LonghornKindBackingImage]; ok {
		return v.(map[string]*longhorn.BackingImage), nil
	}

	bis := map[string]*longhorn.BackingImage{}
	biList, err := lhClient.LonghornV1beta2().BackingImages(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i, bi := range biList.Items {
		bis[bi.Name] = &biList.Items[i]
	}

	resourceMaps[types.LonghornKindBackingImage] = bis

	return bis, nil
}

// ListAndUpdateBackingImageDataSourcesInProvidedCache list all backingImageDataSources and save them into the provided cached `resourceMap`. This method is not thread-safe.
func ListAndUpdateBackingImageDataSourcesInProvidedCache(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}) (map[string]*longhorn.BackingImageDataSource, error) {
	if v, ok := resourceMaps[types.LonghornKindBackingImageDataSource]; ok {
		return v.(map[string]*longhorn.BackingImageDataSource), nil
	}

	bidss := map[string]*longhorn.BackingImageDataSource{}
	bidsList, err := lhClient.LonghornV1beta2().BackingImageDataSources(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i, bids := range bidsList.Items {
		bidss[bids.Name] = &bidsList.Items[i]
	}

	resourceMaps[types.LonghornKindBackingImageDataSource] = bidss

	return bidss, nil
}

// ListAndUpdateBackingImageManagersInProvidedCache list all backingImageManagers and save them into the provided cached `resourceMap`. This method is not thread-safe.
func ListAndUpdateBackingImageManagersInProvidedCache(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}) (map[string]*longhorn.BackingImageManager, error) {
	if v, ok := resourceMaps[types.LonghornKindBackingImageManager]; ok {
		return v.(map[string]*longhorn.BackingImageManager), nil
	}

	bims := map[string]*longhorn.BackingImageManager{}
	bimList, err := lhClient.LonghornV1beta2().BackingImageManagers(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i, bids := range bimList.Items {
		bims[bids.Name] = &bimList.Items[i]
	}

	resourceMaps[types.LonghornKindBackingImageManager] = bims

	return bims, nil
}

// ListAndUpdateRecurringJobsInProvidedCache list all recurringJobs and save them into the provided cached `resourceMap`. This method is not thread-safe.
func ListAndUpdateRecurringJobsInProvidedCache(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}) (map[string]*longhorn.RecurringJob, error) {
	if v, ok := resourceMaps[types.LonghornKindRecurringJob]; ok {
		return v.(map[string]*longhorn.RecurringJob), nil
	}

	recurringJobs := map[string]*longhorn.RecurringJob{}
	recurringJobList, err := lhClient.LonghornV1beta2().RecurringJobs(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i, recurringJob := range recurringJobList.Items {
		recurringJobs[recurringJob.Name] = &recurringJobList.Items[i]
	}

	resourceMaps[types.LonghornKindRecurringJob] = recurringJobs

	return recurringJobs, nil
}

// ListAndUpdateVolumeAttachmentsInProvidedCache list all volumeAttachments and save them into the provided cached `resourceMap`. This method is not thread-safe.
func ListAndUpdateVolumeAttachmentsInProvidedCache(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}) (map[string]*longhorn.VolumeAttachment, error) {
	if v, ok := resourceMaps[types.LonghornKindVolumeAttachment]; ok {
		return v.(map[string]*longhorn.VolumeAttachment), nil
	}

	volumeAttachments := map[string]*longhorn.VolumeAttachment{}
	volumeAttachmentList, err := lhClient.LonghornV1beta2().VolumeAttachments(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i, volumeAttachment := range volumeAttachmentList.Items {
		volumeAttachments[volumeAttachment.Name] = &volumeAttachmentList.Items[i]
	}

	resourceMaps[types.LonghornKindVolumeAttachment] = volumeAttachments

	return volumeAttachments, nil
}

// ListAndUpdateSystemBackupsInProvidedCache list all system backups and save them into the provided cached `resourceMap`. This method is not thread-safe.
func ListAndUpdateSystemBackupsInProvidedCache(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}) (map[string]*longhorn.SystemBackup, error) {
	if v, ok := resourceMaps[types.LonghornKindSystemBackup]; ok {
		return v.(map[string]*longhorn.SystemBackup), nil
	}

	systemBackups := map[string]*longhorn.SystemBackup{}
	systemBackupList, err := lhClient.LonghornV1beta2().SystemBackups(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i, sb := range systemBackupList.Items {
		systemBackups[sb.Name] = &systemBackupList.Items[i]
	}

	resourceMaps[types.LonghornKindSystemBackup] = systemBackups

	return systemBackups, nil
}

// CreateAndUpdateRecurringJobInProvidedCache creates a recurringJob and saves it into the provided cached `resourceMap`. This method is not thread-safe.
func CreateAndUpdateRecurringJobInProvidedCache(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}, job *longhorn.RecurringJob) (*longhorn.RecurringJob, error) {
	obj, err := lhClient.LonghornV1beta2().RecurringJobs(namespace).Create(context.TODO(), job, metav1.CreateOptions{})
	if err != nil {
		return obj, err
	}

	var recurringJobs map[string]*longhorn.RecurringJob
	if v, ok := resourceMaps[types.LonghornKindRecurringJob]; ok {
		recurringJobs = v.(map[string]*longhorn.RecurringJob)
	} else {
		recurringJobs = map[string]*longhorn.RecurringJob{}
	}
	recurringJobs[job.Name] = obj

	resourceMaps[types.LonghornKindRecurringJob] = recurringJobs

	return obj, nil
}

// CreateAndUpdateBackingImageInProvidedCache creates a backingImage and saves it into the provided cached `resourceMap`. This method is not thread-safe.
func CreateAndUpdateBackingImageInProvidedCache(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}, bid *longhorn.BackingImageDataSource) (*longhorn.BackingImageDataSource, error) {
	obj, err := lhClient.LonghornV1beta2().BackingImageDataSources(namespace).Create(context.TODO(), bid, metav1.CreateOptions{})
	if err != nil {
		return obj, err
	}

	var bids map[string]*longhorn.BackingImageDataSource
	if v, ok := resourceMaps[types.LonghornKindBackingImageDataSource]; ok {
		bids = v.(map[string]*longhorn.BackingImageDataSource)
	} else {
		bids = map[string]*longhorn.BackingImageDataSource{}
	}
	bids[bid.Name] = obj

	resourceMaps[types.LonghornKindBackingImageDataSource] = bids

	return obj, nil
}

type untypedUpdateResourceFunc func(namespace string, lhClient *lhclientset.Clientset, resourceMap any, forceWrite bool) error

func toUntypedUpdateResourceFunc[K any](kind string, updater func(namespace string, lhClient *lhclientset.Clientset, resourceMap map[string]K, forceWrite bool) error) untypedUpdateResourceFunc {
	return func(namespace string, lhClient *lhclientset.Clientset, resourceMap any, forceWrite bool) error {
		typedResourceMap, ok := resourceMap.(map[string]K)
		if !ok {
			return fmt.Errorf("unexpected type %T to resource updater %s", resourceMap, kind)
		}
		return updater(namespace, lhClient, typedResourceMap, forceWrite)
	}
}

var resourceUpdaters = map[string]untypedUpdateResourceFunc{
	types.LonghornKindNode:                   toUntypedUpdateResourceFunc(types.LonghornKindNode, updateNodes),
	types.LonghornKindVolume:                 toUntypedUpdateResourceFunc(types.LonghornKindVolume, updateVolumes),
	types.LonghornKindEngine:                 toUntypedUpdateResourceFunc(types.LonghornKindEngine, updateEngines),
	types.LonghornKindReplica:                toUntypedUpdateResourceFunc(types.LonghornKindReplica, updateReplicas),
	types.LonghornKindBackupTarget:           toUntypedUpdateResourceFunc(types.LonghornKindBackupTarget, updateBackupTargets),
	types.LonghornKindBackupVolume:           toUntypedUpdateResourceFunc(types.LonghornKindBackupVolume, updateBackupVolumes),
	types.LonghornKindBackup:                 toUntypedUpdateResourceFunc(types.LonghornKindBackup, updateBackups),
	types.LonghornKindBackupBackingImage:     toUntypedUpdateResourceFunc(types.LonghornKindBackupBackingImage, updateBackupBackingImages),
	types.LonghornKindBackingImageDataSource: toUntypedUpdateResourceFunc(types.LonghornKindBackingImageDataSource, updateBackingImageDataSources),
	types.LonghornKindEngineImage:            toUntypedUpdateResourceFunc(types.LonghornKindEngineImage, updateEngineImages),
	types.LonghornKindInstanceManager:        toUntypedUpdateResourceFunc(types.LonghornKindInstanceManager, updateInstanceManagers),
	types.LonghornKindShareManager:           toUntypedUpdateResourceFunc(types.LonghornKindShareManager, updateShareManagers),
	types.LonghornKindBackingImage:           toUntypedUpdateResourceFunc(types.LonghornKindBackingImage, updateBackingImages),
	types.LonghornKindBackingImageManager:    toUntypedUpdateResourceFunc(types.LonghornKindBackingImageManager, updateBackingImagesManager),
	types.LonghornKindRecurringJob:           toUntypedUpdateResourceFunc(types.LonghornKindRecurringJob, updateRecurringJobs),
	types.LonghornKindSetting:                toUntypedUpdateResourceFunc(types.LonghornKindSetting, updateSettings),
	types.LonghornKindVolumeAttachment:       toUntypedUpdateResourceFunc(types.LonghornKindVolumeAttachment, createOrUpdateVolumeAttachments),
	types.LonghornKindSnapshot:               toUntypedUpdateResourceFunc(types.LonghornKindSnapshot, updateSnapshots),
	types.LonghornKindOrphan:                 toUntypedUpdateResourceFunc(types.LonghornKindOrphan, updateOrphans),
	types.LonghornKindSystemBackup:           toUntypedUpdateResourceFunc(types.LonghornKindSystemBackup, updateSystemBackups),
}

// UpdateResources persists all the resources' spec changes in provided cached `resourceMap`. This method is not thread-safe.
func UpdateResources(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}, forceWrite bool) error {
	for resourceKind, resourceMap := range resourceMaps {
		if updater, ok := resourceUpdaters[resourceKind]; !ok {
			return fmt.Errorf("resource kind %v is not able to updated", resourceKind)
		} else if err := updater(namespace, lhClient, resourceMap, forceWrite); err != nil {
			return err
		}
	}
	return nil
}

func updateNodes(namespace string, lhClient *lhclientset.Clientset, nodes map[string]*longhorn.Node, forceWrite bool) error {
	existingNodeList, err := lhClient.LonghornV1beta2().Nodes(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingNode := range existingNodeList.Items {
		node, ok := nodes[existingNode.Name]
		if !ok {
			continue
		}
		if forceWrite ||
			!reflect.DeepEqual(existingNode.Spec, node.Spec) ||
			!reflect.DeepEqual(existingNode.ObjectMeta, node.ObjectMeta) {
			if _, err = lhClient.LonghornV1beta2().Nodes(namespace).Update(context.TODO(), node, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
				return err
			}
		}
	}
	return nil
}

func updateVolumes(namespace string, lhClient *lhclientset.Clientset, volumes map[string]*longhorn.Volume, forceWrite bool) error {
	existingVolumeList, err := lhClient.LonghornV1beta2().Volumes(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingVolume := range existingVolumeList.Items {
		volume, ok := volumes[existingVolume.Name]
		if !ok {
			continue
		}
		if forceWrite ||
			!reflect.DeepEqual(existingVolume.Spec, volume.Spec) ||
			!reflect.DeepEqual(existingVolume.ObjectMeta, volume.ObjectMeta) {
			if _, err = lhClient.LonghornV1beta2().Volumes(namespace).Update(context.TODO(), volume, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
				return err
			}
		}
	}
	return nil
}

func updateReplicas(namespace string, lhClient *lhclientset.Clientset, replicas map[string]*longhorn.Replica, forceWrite bool) error {
	existingReplicaList, err := lhClient.LonghornV1beta2().Replicas(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingReplica := range existingReplicaList.Items {
		replica, ok := replicas[existingReplica.Name]
		if !ok {
			continue
		}
		if forceWrite ||
			!reflect.DeepEqual(existingReplica.Spec, replica.Spec) ||
			!reflect.DeepEqual(existingReplica.ObjectMeta, replica.ObjectMeta) {
			if _, err = lhClient.LonghornV1beta2().Replicas(namespace).Update(context.TODO(), replica, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
				return err
			}
		}
	}
	return nil
}

func updateEngines(namespace string, lhClient *lhclientset.Clientset, engines map[string]*longhorn.Engine, forceWrite bool) error {
	existingEngineList, err := lhClient.LonghornV1beta2().Engines(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingEngine := range existingEngineList.Items {
		engine, ok := engines[existingEngine.Name]
		if !ok {
			continue
		}
		if forceWrite ||
			!reflect.DeepEqual(existingEngine.Spec, engine.Spec) ||
			!reflect.DeepEqual(existingEngine.ObjectMeta, engine.ObjectMeta) {
			if _, err = lhClient.LonghornV1beta2().Engines(namespace).Update(context.TODO(), engine, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
				return err
			}
		}
	}
	return nil
}

func updateBackupTargets(namespace string, lhClient *lhclientset.Clientset, backupTargets map[string]*longhorn.BackupTarget, forceWrite bool) error {
	existingBackupTargetList, err := lhClient.LonghornV1beta2().BackupTargets(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingBackupTarget := range existingBackupTargetList.Items {
		backupTarget, ok := backupTargets[existingBackupTarget.Name]
		if !ok {
			continue
		}
		if forceWrite ||
			!reflect.DeepEqual(existingBackupTarget.Spec, backupTarget.Spec) ||
			!reflect.DeepEqual(existingBackupTarget.ObjectMeta, backupTarget.ObjectMeta) {
			if _, err = lhClient.LonghornV1beta2().BackupTargets(namespace).Update(context.TODO(), backupTarget, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
				return err
			}
		}
	}
	return nil
}

func updateBackupVolumes(namespace string, lhClient *lhclientset.Clientset, backupVolumes map[string]*longhorn.BackupVolume, forceWrite bool) error {
	existingBackupVolumeList, err := lhClient.LonghornV1beta2().BackupVolumes(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingBackupVolume := range existingBackupVolumeList.Items {
		backupVolume, ok := backupVolumes[existingBackupVolume.Name]
		if !ok {
			continue
		}
		if forceWrite ||
			!reflect.DeepEqual(existingBackupVolume.Spec, backupVolume.Spec) ||
			!reflect.DeepEqual(existingBackupVolume.ObjectMeta, backupVolume.ObjectMeta) {
			if _, err = lhClient.LonghornV1beta2().BackupVolumes(namespace).Update(context.TODO(), backupVolume, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
				return err
			}
		}
	}
	return nil
}

func updateBackups(namespace string, lhClient *lhclientset.Clientset, backups map[string]*longhorn.Backup, forceWrite bool) error {
	existingBackupList, err := lhClient.LonghornV1beta2().Backups(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, existingBackup := range existingBackupList.Items {
		backup, ok := backups[existingBackup.Name]
		if !ok {
			continue
		}
		if forceWrite ||
			!reflect.DeepEqual(existingBackup.Spec, backup.Spec) ||
			!reflect.DeepEqual(existingBackup.ObjectMeta, backup.ObjectMeta) {
			if _, err = lhClient.LonghornV1beta2().Backups(namespace).Update(context.TODO(), backup, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
				return err
			}
		}
	}
	return nil
}

func updateBackupBackingImages(namespace string, lhClient *lhclientset.Clientset, backupBackingImages map[string]*longhorn.BackupBackingImage, forceWrite bool) error {
	existingBackupBackingImageList, err := lhClient.LonghornV1beta2().BackupBackingImages(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingBackupBackingImage := range existingBackupBackingImageList.Items {
		backupBackingImage, ok := backupBackingImages[existingBackupBackingImage.Name]
		if !ok {
			continue
		}
		if forceWrite ||
			!reflect.DeepEqual(existingBackupBackingImage.Spec, backupBackingImage.Spec) ||
			!reflect.DeepEqual(existingBackupBackingImage.ObjectMeta, backupBackingImage.ObjectMeta) {
			if _, err = lhClient.LonghornV1beta2().BackupBackingImages(namespace).Update(context.TODO(), backupBackingImage, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
				return err
			}
		}
	}
	return nil
}

func updateBackingImageDataSources(namespace string, lhClient *lhclientset.Clientset, backingImageDataSources map[string]*longhorn.BackingImageDataSource, forceWrite bool) error {
	existingBackingImageDataSourceList, err := lhClient.LonghornV1beta2().BackingImageDataSources(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingBackingImageDataSource := range existingBackingImageDataSourceList.Items {
		backingImageDataSource, ok := backingImageDataSources[existingBackingImageDataSource.Name]
		if !ok {
			continue
		}
		if forceWrite ||
			!reflect.DeepEqual(existingBackingImageDataSource.Spec, backingImageDataSource.Spec) ||
			!reflect.DeepEqual(existingBackingImageDataSource.ObjectMeta, backingImageDataSource.ObjectMeta) {
			if _, err = lhClient.LonghornV1beta2().BackingImageDataSources(namespace).Update(context.TODO(), backingImageDataSource, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
				return err
			}
		}
	}
	return nil
}

func updateEngineImages(namespace string, lhClient *lhclientset.Clientset, engineImages map[string]*longhorn.EngineImage, forceWrite bool) error {
	existingEngineImageList, err := lhClient.LonghornV1beta2().EngineImages(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingEngineImage := range existingEngineImageList.Items {
		engineImage, ok := engineImages[existingEngineImage.Name]
		if !ok {
			continue
		}
		if forceWrite ||
			!reflect.DeepEqual(existingEngineImage.Spec, engineImage.Spec) ||
			!reflect.DeepEqual(existingEngineImage.ObjectMeta, engineImage.ObjectMeta) {
			if _, err = lhClient.LonghornV1beta2().EngineImages(namespace).Update(context.TODO(), engineImage, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
				return err
			}
		}
	}
	return nil
}

func updateInstanceManagers(namespace string, lhClient *lhclientset.Clientset, instanceManagers map[string]*longhorn.InstanceManager, forceWrite bool) error {
	existingInstanceManagerList, err := lhClient.LonghornV1beta2().InstanceManagers(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingInstanceManager := range existingInstanceManagerList.Items {
		instanceManager, ok := instanceManagers[existingInstanceManager.Name]
		if !ok {
			continue
		}
		if forceWrite ||
			!reflect.DeepEqual(existingInstanceManager.Spec, instanceManager.Spec) ||
			!reflect.DeepEqual(existingInstanceManager.ObjectMeta, instanceManager.ObjectMeta) {
			if _, err = lhClient.LonghornV1beta2().InstanceManagers(namespace).Update(context.TODO(), instanceManager, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
				return err
			}
		}
	}
	return nil
}

func updateShareManagers(namespace string, lhClient *lhclientset.Clientset, shareManagers map[string]*longhorn.ShareManager, forceWrite bool) error {
	existingShareManagerList, err := lhClient.LonghornV1beta2().ShareManagers(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingShareManager := range existingShareManagerList.Items {
		shareManager, ok := shareManagers[existingShareManager.Name]
		if !ok {
			continue
		}
		if forceWrite ||
			!reflect.DeepEqual(existingShareManager.Spec, shareManager.Spec) ||
			!reflect.DeepEqual(existingShareManager.ObjectMeta, shareManager.ObjectMeta) {
			if _, err = lhClient.LonghornV1beta2().ShareManagers(namespace).Update(context.TODO(), shareManager, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
				return err
			}
		}
	}
	return nil
}

func updateBackingImages(namespace string, lhClient *lhclientset.Clientset, backingImages map[string]*longhorn.BackingImage, forceWrite bool) error {
	existingBackingImagesList, err := lhClient.LonghornV1beta2().BackingImages(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingBackingImage := range existingBackingImagesList.Items {
		backingImage, ok := backingImages[existingBackingImage.Name]
		if !ok {
			continue
		}
		if forceWrite ||
			!reflect.DeepEqual(existingBackingImage.Spec, backingImage.Spec) ||
			!reflect.DeepEqual(existingBackingImage.ObjectMeta, backingImage.ObjectMeta) {
			if _, err = lhClient.LonghornV1beta2().BackingImages(namespace).Update(context.TODO(), backingImage, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
				return err
			}
		}
	}
	return nil
}

func updateBackingImagesManager(namespace string, lhClient *lhclientset.Clientset, backingImageManagers map[string]*longhorn.BackingImageManager, forceWrite bool) error {
	existingBackingImageManagersList, err := lhClient.LonghornV1beta2().BackingImageManagers(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingBackingImageManager := range existingBackingImageManagersList.Items {
		backingImageManager, ok := backingImageManagers[existingBackingImageManager.Name]
		if !ok {
			continue
		}
		if forceWrite ||
			!reflect.DeepEqual(existingBackingImageManager.Spec, backingImageManager.Spec) ||
			!reflect.DeepEqual(existingBackingImageManager.ObjectMeta, backingImageManager.ObjectMeta) {
			if _, err = lhClient.LonghornV1beta2().BackingImageManagers(namespace).Update(context.TODO(), backingImageManager, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
				return err
			}
		}
	}
	return nil
}

func updateRecurringJobs(namespace string, lhClient *lhclientset.Clientset, recurringJobs map[string]*longhorn.RecurringJob, forceWrite bool) error {
	existingRecurringJobList, err := lhClient.LonghornV1beta2().RecurringJobs(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingRecurringJob := range existingRecurringJobList.Items {
		recurringJob, ok := recurringJobs[existingRecurringJob.Name]
		if !ok {
			continue
		}
		if forceWrite ||
			!reflect.DeepEqual(existingRecurringJob.Spec, recurringJob.Spec) ||
			!reflect.DeepEqual(existingRecurringJob.ObjectMeta, recurringJob.ObjectMeta) {
			if _, err = lhClient.LonghornV1beta2().RecurringJobs(namespace).Update(context.TODO(), recurringJob, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
				return err
			}
		}
	}
	return nil
}

func updateSettings(namespace string, lhClient *lhclientset.Clientset, settings map[string]*longhorn.Setting, forceWrite bool) error {
	existingSettingList, err := lhClient.LonghornV1beta2().Settings(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, existingSetting := range existingSettingList.Items {
		setting, ok := settings[existingSetting.Name]
		if !ok {
			continue
		}

		if forceWrite || !reflect.DeepEqual(existingSetting.Value, setting.Value) {
			if setting.Annotations == nil {
				setting.Annotations = make(map[string]string)
			}
			setting.Annotations[types.GetLonghornLabelKey(types.UpdateSettingFromLonghorn)] = ""
			setting, err = lhClient.LonghornV1beta2().Settings(namespace).Update(context.TODO(), setting, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict})
			if err != nil {
				return err
			}
			delete(setting.Annotations, types.GetLonghornLabelKey(types.UpdateSettingFromLonghorn))
			if _, err = lhClient.LonghornV1beta2().Settings(namespace).Update(context.TODO(), setting, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
				return err
			}
		}
	}

	return nil
}

func updateSnapshots(namespace string, lhClient *lhclientset.Clientset, snapshots map[string]*longhorn.Snapshot, forceWrite bool) error {
	existingSnapshotList, err := lhClient.LonghornV1beta2().Snapshots(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingSnapshot := range existingSnapshotList.Items {
		snapshot, ok := snapshots[existingSnapshot.Name]
		if !ok {
			continue
		}
		if forceWrite ||
			!reflect.DeepEqual(existingSnapshot.Spec, snapshot.Spec) ||
			!reflect.DeepEqual(existingSnapshot.ObjectMeta, snapshot.ObjectMeta) {
			if _, err = lhClient.LonghornV1beta2().Snapshots(namespace).Update(context.TODO(), snapshot, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
				return err
			}
		}
	}
	return nil
}

func updateOrphans(namespace string, lhClient *lhclientset.Clientset, orphans map[string]*longhorn.Orphan, forceWrite bool) error {
	existingOrphanList, err := lhClient.LonghornV1beta2().Orphans(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingOrphan := range existingOrphanList.Items {
		orphan, ok := orphans[existingOrphan.Name]
		if !ok {
			continue
		}
		if forceWrite ||
			!reflect.DeepEqual(existingOrphan.Spec, orphan.Spec) ||
			!reflect.DeepEqual(existingOrphan.ObjectMeta, orphan.ObjectMeta) {
			if _, err = lhClient.LonghornV1beta2().Orphans(namespace).Update(context.TODO(), orphan, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
				return err
			}
		}
	}
	return nil
}

func updateSystemBackups(namespace string, lhClient *lhclientset.Clientset, systemBackups map[string]*longhorn.SystemBackup, forceWrite bool) error {
	existingSystemBackupList, err := lhClient.LonghornV1beta2().SystemBackups(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingSystemBackup := range existingSystemBackupList.Items {
		sb, ok := systemBackups[existingSystemBackup.Name]
		if !ok {
			continue
		}
		if forceWrite ||
			!reflect.DeepEqual(existingSystemBackup.Spec, sb.Spec) ||
			!reflect.DeepEqual(existingSystemBackup.ObjectMeta, sb.ObjectMeta) {
			if _, err = lhClient.LonghornV1beta2().SystemBackups(namespace).Update(context.TODO(), sb, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
				return err
			}
		}
	}
	return nil
}

func createOrUpdateVolumeAttachments(namespace string, lhClient *lhclientset.Clientset, volumeAttachments map[string]*longhorn.VolumeAttachment, forceWrite bool) error {
	existingVolumeAttachmentList, err := lhClient.LonghornV1beta2().VolumeAttachments(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	existingVolumeAttachmentMap := map[string]longhorn.VolumeAttachment{}
	for _, va := range existingVolumeAttachmentList.Items {
		existingVolumeAttachmentMap[va.Name] = va
	}

	for _, va := range volumeAttachments {
		if existingVolumeAttachment, ok := existingVolumeAttachmentMap[va.Name]; ok {
			if !reflect.DeepEqual(existingVolumeAttachment.Spec, va.Spec) ||
				!reflect.DeepEqual(existingVolumeAttachment.ObjectMeta, va.ObjectMeta) {
				if _, err = lhClient.LonghornV1beta2().VolumeAttachments(namespace).Update(context.TODO(), va, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
					return err
				}
			}
		} else {
			if _, err = lhClient.LonghornV1beta2().VolumeAttachments(namespace).Create(context.TODO(), va, metav1.CreateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
				return err
			}
		}
	}

	return nil
}

// UpdateResourcesStatus persists all the resources' status changes in provided cached `resourceMap`. This method is not thread-safe.
func UpdateResourcesStatus(namespace string, lhClient *lhclientset.Clientset, resourceMaps map[string]interface{}) error {
	var err error

	for resourceKind, resourceMap := range resourceMaps {
		switch resourceKind {
		case types.LonghornKindNode:
			err = updateNodesStatus(namespace, lhClient, resourceMap.(map[string]*longhorn.Node))
		case types.LonghornKindEngineImage:
			err = updateEngineImageStatus(namespace, lhClient, resourceMap.(map[string]*longhorn.EngineImage))
		case types.LonghornKindEngine:
			err = updateEngineStatus(namespace, lhClient, resourceMap.(map[string]*longhorn.Engine))
		case types.LonghornKindSetting:
			err = updateSettingStatus(namespace, lhClient, resourceMap.(map[string]*longhorn.Setting))
		case types.LonghornKindBackup:
			err = updateBackupStatus(namespace, lhClient, resourceMap.(map[string]*longhorn.Backup))
		case types.LonghornKindBackingImage:
			err = updateBackingImageStatus(namespace, lhClient, resourceMap.(map[string]*longhorn.BackingImage))
		default:
			return fmt.Errorf("resource kind %v is not able to updated", resourceKind)
		}

		if err != nil {
			return err
		}
	}

	return nil
}

func updateNodesStatus(namespace string, lhClient *lhclientset.Clientset, nodes map[string]*longhorn.Node) error {
	existingNodeList, err := lhClient.LonghornV1beta2().Nodes(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingNode := range existingNodeList.Items {
		node, ok := nodes[existingNode.Name]
		if !ok {
			continue
		}
		if !reflect.DeepEqual(existingNode.Status, node.Status) {
			if _, err = lhClient.LonghornV1beta2().Nodes(namespace).UpdateStatus(context.TODO(), node, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
				return err
			}
		}
	}
	return nil
}

func updateEngineImageStatus(namespace string, lhClient *lhclientset.Clientset, eis map[string]*longhorn.EngineImage) error {
	existingEngineImageList, err := lhClient.LonghornV1beta2().EngineImages(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingEngineImage := range existingEngineImageList.Items {
		ei, ok := eis[existingEngineImage.Name]
		if !ok {
			continue
		}

		if _, err = lhClient.LonghornV1beta2().EngineImages(namespace).UpdateStatus(context.TODO(), ei, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
			return err
		}
	}
	return nil
}

func updateEngineStatus(namespace string, lhClient *lhclientset.Clientset, engines map[string]*longhorn.Engine) error {
	existingEngineList, err := lhClient.LonghornV1beta2().Engines(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingEngine := range existingEngineList.Items {
		engine, ok := engines[existingEngine.Name]
		if !ok {
			continue
		}

		if _, err = lhClient.LonghornV1beta2().Engines(namespace).UpdateStatus(context.TODO(), engine, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
			return err
		}
	}
	return nil
}

func updateSettingStatus(namespace string, lhClient *lhclientset.Clientset, settings map[string]*longhorn.Setting) error {
	existingSettingList, err := lhClient.LonghornV1beta2().Settings(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingSetting := range existingSettingList.Items {
		setting, ok := settings[existingSetting.Name]
		if !ok {
			continue
		}

		if _, err = lhClient.LonghornV1beta2().Settings(namespace).UpdateStatus(context.TODO(), setting, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
			return err
		}
	}
	return nil
}

func updateBackupStatus(namespace string, lhClient *lhclientset.Clientset, backups map[string]*longhorn.Backup) error {
	existingBackupList, err := lhClient.LonghornV1beta2().Backups(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingBackup := range existingBackupList.Items {
		backup, ok := backups[existingBackup.Name]
		if !ok {
			continue
		}

		if _, err = lhClient.LonghornV1beta2().Backups(namespace).UpdateStatus(context.TODO(), backup, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
			return err
		}
	}
	return nil
}

func updateBackingImageStatus(namespace string, lhClient *lhclientset.Clientset, backingImages map[string]*longhorn.BackingImage) error {
	existingBackingImageList, err := lhClient.LonghornV1beta2().BackingImages(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, existingBackingImage := range existingBackingImageList.Items {
		bi, ok := backingImages[existingBackingImage.Name]
		if !ok {
			continue
		}

		if _, err = lhClient.LonghornV1beta2().BackingImages(namespace).UpdateStatus(context.TODO(), bi, metav1.UpdateOptions{FieldValidation: metav1.FieldValidationStrict}); err != nil {
			return err
		}
	}
	return nil
}
