package engineapi

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/pkg/errors"

	"github.com/rancher/longhorn-manager/util"
)

const (
	VolumeHeadName = "volume-head"
	purgeTimeout   = 15 * time.Minute
	backupTimeout  = 360 * time.Minute
)

func (e *Engine) SnapshotCreate(name string, labels map[string]string) (string, error) {
	args := []string{"--url", e.cURL, "snapshot", "create"}
	for k, v := range labels {
		args = append(args, "--label", k+"="+v)
	}
	args = append(args, name)

	output, err := util.Execute(LonghornEngineBinary, args...)
	if err != nil {
		return "", errors.Wrapf(err, "error creating snapshot '%s'", name)
	}
	return strings.TrimSpace(output), nil
}

func (e *Engine) SnapshotList() (map[string]*Snapshot, error) {
	args := []string{"--url", e.cURL, "snapshot", "info"}
	output, err := util.Execute(LonghornEngineBinary, args...)
	if err != nil {
		return nil, errors.Wrapf(err, "error listing snapshot")
	}
	data := map[string]*Snapshot{}
	if err := json.Unmarshal([]byte(output), &data); err != nil {
		return nil, errors.Wrapf(err, "error parsing snapshot list")
	}
	delete(data, VolumeHeadName)
	return data, nil
}

func (e *Engine) SnapshotGet(name string) (*Snapshot, error) {
	data, err := e.SnapshotList()
	if err != nil {
		return nil, err
	}
	return data[name], nil
}

func (e *Engine) SnapshotDelete(name string) error {
	if _, err := util.Execute(LonghornEngineBinary, "--url", e.cURL,
		"snapshot", "rm", name); err != nil {
		return errors.Wrapf(err, "error deleting snapshot '%s'", name)
	}
	return nil
}

func (e *Engine) SnapshotRevert(name string) error {
	if _, err := util.Execute(LonghornEngineBinary, "--url", e.cURL,
		"snapshot", "revert", name); err != nil {
		return errors.Wrapf(err, "error reverting to snapshot '%s'", name)
	}
	return nil
}

func (e *Engine) SnapshotPurge() error {
	if _, err := util.ExecuteWithTimeout(purgeTimeout, LonghornEngineBinary, "--url", e.cURL,
		"snapshot", "purge"); err != nil {
		return errors.Wrapf(err, "error purging snapshots")
	}
	logrus.Debugf("Volume %v snapshot purge completed", e.Name())
	return nil
}

func (e *Engine) SnapshotBackup(snapName, backupTarget string, labels map[string]string) error {
	snap, err := e.SnapshotGet(snapName)
	if err != nil {
		return errors.Wrapf(err, "error getting snapshot '%s', volume '%s'", snapName, e.name)
	}
	if snap == nil {
		return errors.Errorf("could not find snapshot '%s' to backup, volume '%s'", snapName, e.name)
	}
	args := []string{"--url", e.cURL, "backup", "create", "--dest", backupTarget}
	for k, v := range labels {
		args = append(args, "--label", k+"="+v)
	}
	args = append(args, snapName)
	backup, err := util.ExecuteWithTimeout(backupTimeout, LonghornEngineBinary, args...)
	if err != nil {
		return err
	}
	logrus.Debugf("Backup %v created for volume %v snapshot %v", backup, e.Name(), snapName)
	return nil
}
