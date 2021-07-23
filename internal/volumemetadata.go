package internal

import (
	"os"
	"path"
	"strconv"

	"encoding/json"
	"io/ioutil"

	"k8s.io/klog/v2"
)

const (
	metadataFile = ".kube-efs-provisioner-metadata"
)

type VolumeMetadata struct {
	GID              string `json:"gid"`
	PVCName          string `json:"pvcName"`
	PVCNamespace     string `json:"pvcNamespace"`
	StorageClassName string `json:"storageClassName"`
}

func (v VolumeMetadata) GidAsUInt() (uint32, error) {
	gid, err := strconv.ParseUint(v.GID, 10, 32)
	if err != nil {
		return 0, err
	}

	return uint32(gid), nil
}

// WriteVolumeMetadata writes a serialized version of the given VolumeMetadata object to the given directory.
func WriteVolumeMetadata(dir string, md VolumeMetadata) error {
	mdpath := getMetaDataPath(dir)

	contents, err := json.MarshalIndent(md, "", "  ")
	if err != nil {
		klog.Errorf("failed to marshal metadata: %v", err)
		return err
	}

	if err := ioutil.WriteFile(mdpath, contents, 0600); err != nil {
		klog.Errorf("failed to write metadata file %v: %v", mdpath, err)
		return err
	}

	return nil
}

// ReadVolumeMetadata reads the metadata file in the given directory and returns a *VolumeMetadata with the contents.
func ReadVolumeMetadata(dir string) (*VolumeMetadata, error) {
	md := &VolumeMetadata{}
	mdpath := getMetaDataPath(dir)

	r, err := os.Open(mdpath)
	if os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		klog.Errorf("failed to read metadata file %v: %v", mdpath, err)
		return nil, err
	}
	defer r.Close()

	dec := json.NewDecoder(r)
	if err := dec.Decode(md); err != nil {
		klog.Errorf("failed to unmarshal %v: %v", mdpath, err)
		return nil, err
	}

	return md, nil
}

func getMetaDataPath(dir string) string {
	return path.Join(dir, metadataFile)
}
