package api

import (
	"net/http"

	"github.com/gorilla/mux"
	"github.com/pkg/errors"

	"github.com/rancher/go-rancher/api"
)

func (s *Server) BackupBackingImageList(rw http.ResponseWriter, req *http.Request) (err error) {
	apiContext := api.GetApiContext(req)
	backupBackingImages, err := s.m.ListBackupBackingImagesSorted()
	if err != nil {
		return errors.Wrap(err, "failed to list backup backing images")
	}
	apiContext.Write(toBackupBackingImageCollection(backupBackingImages, apiContext))
	return nil
}

func (s *Server) BackupBackingImageGet(w http.ResponseWriter, req *http.Request) error {
	apiContext := api.GetApiContext(req)
	backupBackingImageName := mux.Vars(req)["name"]
	backupBackingImage, err := s.m.GetBackupBackingImage(backupBackingImageName)
	if err != nil {
		return errors.Wrapf(err, "failed to get backup backing image '%s'", backupBackingImageName)
	}

	apiContext.Write(toBackupBackingImageResource(backupBackingImage, apiContext))
	return nil
}

func (s *Server) BackupBackingImageDelete(w http.ResponseWriter, req *http.Request) error {
	backupBackingImageName := mux.Vars(req)["name"]
	if err := s.m.DeleteBackupBackingImage(backupBackingImageName); err != nil {
		return errors.Wrapf(err, "failed to delete backup backing image '%s'", backupBackingImageName)
	}
	return nil
}

func (s *Server) BackupBackingImageRestore(w http.ResponseWriter, req *http.Request) error {
	backupBackingImageName := mux.Vars(req)["name"]
	if err := s.m.RestoreBackupBackingImage(backupBackingImageName); err != nil {
		return errors.Wrapf(err, "failed to restore backup backing image '%s'", backupBackingImageName)
	}
	return nil
}

func (s *Server) BackupBackingImageCreate(w http.ResponseWriter, req *http.Request) error {
	var input BackupBackingImage

	apiContext := api.GetApiContext(req)
	if err := apiContext.Read(&input); err != nil {
		return err
	}

	backingImageName := mux.Vars(req)["name"]
	if err := s.m.CreateBackupBackingImage(input.Name, backingImageName, input.BackupTargetName, input.BackupTargetURL); err != nil {
		return errors.Wrapf(err, "failed to create backup backing image '%s'", input.Name)
	}
	return nil
}
