/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// This file is a modified fork of https://github.com/kubernetes-retired/external-storage/blob/master/aws/efs/cmd/efs-provisioner/efs-provisioner.go

package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"

	"github.com/OneCause/efs-provisioner/internal"
	"sigs.k8s.io/sig-storage-lib-external-provisioner/v7/util"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/efs"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	"sigs.k8s.io/sig-storage-lib-external-provisioner/v7/controller"
	"sigs.k8s.io/sig-storage-lib-external-provisioner/v7/gidallocator"
	"sigs.k8s.io/sig-storage-lib-external-provisioner/v7/mount"
)

const (
	provisionerNameKey = "PROVISIONER_NAME"
	fileSystemIDKey    = "FILE_SYSTEM_ID"
	awsRegionKey       = "AWS_REGION"
	dnsNameKey         = "DNS_NAME"
)

var _ controller.Provisioner = &efsProvisioner{}

type efsProvisioner struct {
	dnsName    string
	mountpoint string
	source     string
	allocator  gidallocator.Allocator
}

// NewEFSProvisioner creates an AWS EFS volume provisioner
func NewEFSProvisioner(client kubernetes.Interface) controller.Provisioner {
	fileSystemID := os.Getenv(fileSystemIDKey)
	if fileSystemID == "" {
		klog.Fatalf("environment variable %s is not set! Please set it.", fileSystemIDKey)
	}

	awsRegion := os.Getenv(awsRegionKey)
	if awsRegion == "" {
		klog.Fatalf("environment variable %s is not set! Please set it.", awsRegionKey)
	}

	dnsName := os.Getenv(dnsNameKey)
	klog.Errorf("%s", dnsName)
	if dnsName == "" {
		dnsName = getDNSName(fileSystemID, awsRegion)
	}

	mountpoint, source, err := getMount(dnsName)
	if err != nil {
		klog.Fatal(err)
	}

	sess, err := session.NewSession()
	if err != nil {
		klog.Warningf("couldn't create an AWS session: %v", err)
	}

	svc := efs.New(sess, &aws.Config{Region: aws.String(awsRegion)})
	params := &efs.DescribeFileSystemsInput{
		FileSystemId: aws.String(fileSystemID),
	}

	_, err = svc.DescribeFileSystems(params)
	if err != nil {
		klog.Warningf("couldn't confirm that the EFS file system exists: %v", err)
	}

	return &efsProvisioner{
		dnsName:    dnsName,
		mountpoint: mountpoint,
		source:     source,
		allocator:  gidallocator.NewWithGIDReclaimer(client, internal.NewFileSystemReclaimer(mountpoint)),
	}
}

func getDNSName(fileSystemID, awsRegion string) string {
	return fileSystemID + ".efs." + awsRegion + ".amazonaws.com"
}

func getMount(dnsName string) (string, string, error) {
	entries, err := mount.GetMounts()
	if err != nil {
		return "", "", err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Source, dnsName) {
			return e.Mountpoint, e.Source, nil
		}
	}

	entriesStr := ""
	for _, e := range entries {
		entriesStr += e.Source + ":" + e.Mountpoint + ", "
	}
	return "", "", fmt.Errorf("no mount entry found for %s among entries %s", dnsName, entriesStr)
}

func reuseVolumesOption(options controller.ProvisionOptions) (bool, error) {
	if reuseStr, ok := options.StorageClass.Parameters["reuseVolumes"]; ok {
		reuse, err := strconv.ParseBool(reuseStr)
		if err != nil {
			return false, fmt.Errorf("invalid value '%s' for parameter reuseVolumes: %v", reuseStr, err)
		}
		return reuse, nil
	}
	return false, nil
}

// Provision creates a storage asset and returns a PV object representing it.
func (p *efsProvisioner) Provision(_ context.Context, options controller.ProvisionOptions) (*v1.PersistentVolume, controller.ProvisioningState, error) {
	if options.PVC.Spec.Selector != nil {
		return nil, controller.ProvisioningNoChange, fmt.Errorf("claim.Spec.Selector is not supported")
	}

	volumePath, err := p.getLocalPath(options)
	if err != nil {
		klog.Errorf("Failed to provision volume: %v", err)
		return nil, controller.ProvisioningNoChange, err
	}

	klog.Infof("provisioning volume at %s", volumePath)

	volExists := false
	var existingGid uint32
	var gid *int
	var reuseVolumes bool

	if reuseVolumes, err = reuseVolumesOption(options); err != nil {
		klog.Errorf("%v", err)
		return nil, controller.ProvisioningNoChange, err
	}

	if reuseVolumes {
		volExists, existingGid, err = internal.VolumeExists(volumePath) // existingGid is the actual gid on the directory in the file system
		if err != nil {
			return nil, controller.ProvisioningNoChange, err
		}
	}

	// hook back up to existing directory if we are configured to reuse volumes, the volume exists, and its metadata matches the current PVC and storageclass.
	if volExists {
		klog.Infof("%s already exists", volumePath)

		md, err := internal.ReadVolumeMetadata(volumePath)
		if err != nil {
			return nil, controller.ProvisioningNoChange, internal.LogErrorf("failed to read volume metadata for %s: %v", volumePath, err)
		}

		err = internal.ValidatePreexistingVolume(options, md, volumePath, existingGid)
		if err != nil {
			return nil, controller.ProvisioningNoChange, err
		}

		// if a GID was previously allocated and it matches the actual GID on the current directory then use it
		if md.GID != "" {
			existingGidInt := int(existingGid)
			mdGidInt, err := strconv.Atoi(md.GID)
			if err != nil {
				return nil, controller.ProvisioningNoChange, internal.LogErrorf("volume metadata contains an invalid GID value: %d", md.GID)
			}

			if existingGidInt == mdGidInt {
				gid = &existingGidInt
			} else {
				return nil, controller.ProvisioningNoChange, internal.LogErrorf("directory %s has a GID of %d, but the volume metadata shows the GID as %s", volumePath, existingGid, md.GID)
			}
		}

		klog.Infof("%s was reused since the preexisting volume metadata matches the PVC", volumePath)
	} else {
		gidAllocate := true
		for k, v := range options.StorageClass.Parameters {
			switch strings.ToLower(k) {
			case "gidmin":
				// Let allocator handle
			case "gidmax":
				// Let allocator handle
			case "gidallocate":
				b, err := strconv.ParseBool(v)
				if err != nil {
					return nil, controller.ProvisioningNoChange, fmt.Errorf("invalid value %s for parameter %s: %v", v, k, err)
				}
				gidAllocate = b
			}
		}

		if gidAllocate {
			allocate, err := p.allocator.AllocateNext(options)
			if err != nil {
				return nil, controller.ProvisioningNoChange, err
			}
			gid = &allocate
		}

		err := p.createVolume(volumePath, gid)
		if err != nil {
			return nil, controller.ProvisioningNoChange, err
		}

		var gidstr string
		if gid != nil {
			gidstr = strconv.Itoa(*gid)
		}

		if reuseVolumes {
			internal.WriteVolumeMetadata(volumePath,
				internal.VolumeMetadata{
					GID:              gidstr,
					PVCName:          options.PVC.Name,
					PVCNamespace:     options.PVC.Namespace,
					StorageClassName: util.GetPersistentVolumeClaimClass(options.PVC),
				})
		}
	}

	mountOptions := []string{"vers=4.1"}
	if options.StorageClass.MountOptions != nil {
		mountOptions = options.StorageClass.MountOptions
	}

	remotePath, err := p.getRemotePath(options)
	if err != nil {
		klog.Errorf("failed to get remote path: %s", err)
		return nil, controller.ProvisioningNoChange, err
	}

	pv := &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			// TODO: if the storage class is configured to reuse existing volumes, should we use a predictable name for the PV so that an existing
			// one in Released status will get reused (similar for how we handle directories)?  Right now a new one will be made, which doesn't seem
			// to hurt anything, but it might be weird to continue to have a released volume sit there forever.  If we would opt to reuse the released
			// PV, then we would either need to modify the controller to pass us a different name, or ignore the name it passed us and use our own.
			Name: options.PVName,
		},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: *options.StorageClass.ReclaimPolicy,
			AccessModes:                   options.PVC.Spec.AccessModes,
			Capacity: v1.ResourceList{
				v1.ResourceName(v1.ResourceStorage): options.PVC.Spec.Resources.Requests[v1.ResourceName(v1.ResourceStorage)],
			},
			PersistentVolumeSource: v1.PersistentVolumeSource{
				NFS: &v1.NFSVolumeSource{
					Server:   p.dnsName,
					Path:     remotePath,
					ReadOnly: false,
				},
			},
			MountOptions: mountOptions,
		},
	}

	if gid != nil {
		pv.ObjectMeta.Annotations = map[string]string{
			gidallocator.VolumeGidAnnotationKey: strconv.FormatInt(int64(*gid), 10),
		}
	}

	return pv, controller.ProvisioningFinished, nil
}

func (p *efsProvisioner) createVolume(path string, gid *int) error {
	perm := os.FileMode(0777)
	if gid != nil {
		perm = os.FileMode(0771 | os.ModeSetgid)
	}

	if err := os.MkdirAll(path, perm); err != nil {
		return err
	}

	// Due to umask, need to chmod
	if err := os.Chmod(path, perm); err != nil {
		os.RemoveAll(path)
		return err
	}

	if gid != nil {
		cmd := exec.Command("chgrp", strconv.Itoa(*gid), path)
		out, err := cmd.CombinedOutput()
		if err != nil {
			os.RemoveAll(path)
			return fmt.Errorf("chgrp failed with error: %v, output: %s", err, out)
		}
	}

	return nil
}

func (p *efsProvisioner) getLocalPath(options controller.ProvisionOptions) (string, error) {
	dirname, err := p.getDirectoryName(options)
	if err != nil {
		return "", err
	}
	return path.Join(p.mountpoint, dirname), nil
}

func (p *efsProvisioner) getRemotePath(options controller.ProvisionOptions) (string, error) {
	dirname, err := p.getDirectoryName(options)
	if err != nil {
		return "", err
	}
	sourcePath := path.Clean(strings.Replace(p.source, p.dnsName+":", "", 1))
	return path.Join(sourcePath, dirname), nil
}

// getDirectoryName determines the name of the directory to create for the PVC.
// If we are in "reuse volumes" mode, then we generate a predictable name so that
// the same PVC will always result in the same directory name.  Otherwise, we generate
// a unique name using the name of the generated PV
func (p *efsProvisioner) getDirectoryName(options controller.ProvisionOptions) (string, error) {
	reuseVolumes, err := reuseVolumesOption(options)
	if err != nil {
		return "", err
	}

	if reuseVolumes {
		prefix := options.StorageClass.Parameters["volumePrefix"]
		if prefix != "" {
			prefix = prefix + "-"
		}

		return prefix + options.PVC.Name + "-" + options.PVC.Namespace, nil
	}

	return options.PVC.Name + "-" + options.PVName, nil
}

// Delete removes the storage asset that was created by Provision represented
// by the given PV.
func (p *efsProvisioner) Delete(_ context.Context, volume *v1.PersistentVolume) error {
	//TODO ignorederror
	err := p.allocator.Release(volume)
	if err != nil {
		return err
	}

	path, err := p.getLocalPathToDelete(volume.Spec.NFS)
	if err != nil {
		return err
	}

	klog.Infof("Deleting %s", path)

	if err := os.RemoveAll(path); err != nil {
		return err
	}

	return nil
}

func (p *efsProvisioner) getLocalPathToDelete(nfs *v1.NFSVolumeSource) (string, error) {
	if nfs.Server != p.dnsName {
		return "", fmt.Errorf("volume's NFS server %s is not equal to the server %s from which this provisioner creates volumes", nfs.Server, p.dnsName)
	}

	sourcePath := path.Clean(strings.Replace(p.source, p.dnsName+":", "", 1))
	if !strings.HasPrefix(nfs.Path, sourcePath) {
		return "", fmt.Errorf("volume's NFS path %s is not a child of the server path %s mounted in this provisioner at %s", nfs.Path, p.source, p.mountpoint)
	}

	subpath := strings.Replace(nfs.Path, sourcePath, "", 1)

	return path.Join(p.mountpoint, subpath), nil
}

// buildKubeConfig builds REST config based on master URL and kubeconfig path.
// If both of them are empty then in cluster config is used.
func buildKubeConfig() (*rest.Config, error) {
	kubeconfig := os.Getenv("KUBECONFIG")
	master := os.Getenv("KUBE_MASTER_URL")

	if master != "" || kubeconfig != "" {
		klog.Infof("Either master or kubeconfig specified. building kube config from that..")
		return clientcmd.BuildConfigFromFlags(master, kubeconfig)
	} else {
		klog.Infof("Building kube config for running in cluster...")
		return rest.InClusterConfig()
	}
}

func Execute() {
	flag.Parse()
	flag.Set("logtostderr", "true")

	klog.Info("Starting efs-provisioner")

	// Create an InClusterConfig and use it to create a client for the controller
	// to use to communicate with Kubernetes
	config, err := buildKubeConfig()
	if err != nil {
		klog.Fatalf("Failed to create config: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Fatalf("Failed to create client: %v", err)
	}

	// Create the provisioner: it implements the Provisioner interface expected by
	// the controller
	efsProvisioner := NewEFSProvisioner(clientset)

	provisionerName := os.Getenv(provisionerNameKey)
	if provisionerName == "" {
		klog.Fatalf("environment variable %s is not set! Please set it.", provisionerNameKey)
	}

	// Start the provision controller which will dynamically provision efs NFS
	// PVs
	pc := controller.NewProvisionController(
		clientset,
		provisionerName,
		efsProvisioner,
	)

	klog.Info("Starting provisioner controller")

	pc.Run(context.Background())
}
