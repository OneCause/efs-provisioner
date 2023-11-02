package internal

import (
	"io/ioutil"
	"os"
	"path"
	"strconv"
	"syscall"

	"k8s.io/klog/v2"
	"sigs.k8s.io/sig-storage-lib-external-provisioner/v9/allocator"
	"sigs.k8s.io/sig-storage-lib-external-provisioner/v9/controller"
	"sigs.k8s.io/sig-storage-lib-external-provisioner/v9/gidreclaimer"
	"sigs.k8s.io/sig-storage-lib-external-provisioner/v9/util"
)

// compile time check to make sure FileSystemReclaimer implements the GIDReclaimer interface
var _ gidreclaimer.GIDReclaimer = &FileSystemReclaimer{}

func NewFileSystemReclaimer(basePath string) *FileSystemReclaimer {
	return &FileSystemReclaimer{BasePath: basePath}
}

type FileSystemReclaimer struct {
	BasePath string
}

// Reclaim looks at every top level directory in the basepath and adds its gid to the given gidTable
func (f *FileSystemReclaimer) Reclaim(classname string, gidtable *allocator.MinMaxAllocator) error {
	klog.Infof("adding gids for any existing directories under %s to the gid table", f.BasePath)

	entries, err := ioutil.ReadDir(f.BasePath)
	if err != nil {
		klog.Errorf("failed to list contents of %s: %v", f.BasePath, err)
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		mddir := path.Join(f.BasePath, entry.Name())

		md, err := ReadVolumeMetadata(mddir)
		if err != nil {
			klog.Warningf("failed to read volume metadata for %s: %v", mddir, err)
			continue
		}

		// if no metadata then it must have been created by another storage class that doesn't have reuseVolumes set since those don't write metadata
		if md == nil {
			continue
		}

		// skip volumes for other storage classes
		if md.StorageClassName != classname {
			continue
		}

		// no GID was previously allocated
		if md.GID == "" {
			continue
		}

		gid, err := strconv.Atoi(md.GID)
		if err != nil {
			klog.Errorf("invalid GID value '%s' in metadata for %s", md.GID, mddir)
			continue
		}

		_, err = gidtable.Allocate(gid)
		if err == allocator.ErrConflict {
			klog.Infof("gid %d found in %s was already allocated for storageclass %s", gid, mddir, classname)
			continue
		} else if err != nil {
			klog.Errorf("failed to store GID %d found in metadata for %s: %v", gid, mddir, err)
			continue
		}
	}

	return nil
}

// VolumeExists determines if the given directory already exists, and if so returns the GID
func VolumeExists(path string) (bool, uint32, error) {
	if stat, err := os.Stat(path); err == nil {
		// not likely to occur unless someone is doing something weird
		if !stat.IsDir() {
			return false, 0, LogErrorf("%s already exists but is a file: %v", path, err)
		}

		return true, stat.Sys().(*syscall.Stat_t).Gid, nil
	} else if os.IsNotExist(err) {
		return false, 0, nil
	} else {
		return false, 0, LogErrorf("Failed to determine if %s already exists: %v", path, err)
	}
}

// ValidatePreexistingVolume determines if the preexisting directory originally came from the new PVC that is being deployed
// based on the contents of the metadata file stored in the directory.  If the storage class, PCV name, PVC namespace, and GID all match,
// then we assume the PVC now being deployed previously must have resulted in this directory being created because the PVC was deleted,
// but the directory wasn't (maybe because the reclaim policy on the storage class was set to Retain, or maybe because the entire Kubernetes
// cluster was destroyed and recreated but the same EFS was reused for the cluster).
func ValidatePreexistingVolume(options controller.ProvisionOptions, md *VolumeMetadata, volumePath string, existingGID uint32) error {
	if md == nil {
		return LogErrorf("%s already exists but has no volume metadata", volumePath)
	}

	class := util.GetPersistentVolumeClaimClass(options.PVC)
	if md.StorageClassName != class {
		return LogErrorf("%s already exists but was created for storage class %s instead of the currently requested storage class of %s",
			volumePath, md.StorageClassName, class)
	}

	if md.PVCName != options.PVC.Name || md.PVCNamespace != options.PVC.Namespace {
		return LogErrorf("%s already exists but was created for storage class %s/%s instead of the currently requested storage class of %s/%s",
			volumePath, md.PVCNamespace, md.PVCName, options.PVC.Namespace, class)
	}

	if md.GID != "" {
		mdgid, err := md.GidAsUInt()
		if err != nil {
			return LogErrorf("metadata for %s contains an invalid gid value '%s'", volumePath, md.GID)
		}

		if existingGID != mdgid {
			return LogErrorf("%s already exists, but its gid is %d while the volume metadata says the gid should be %d", volumePath, existingGID, mdgid)
		}
	}

	return nil
}
