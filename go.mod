module github.com/OneCause/efs-provisioner

go 1.16

replace sigs.k8s.io/sig-storage-lib-external-provisioner/v7 v7.0.1 => github.com/OneCause/sig-storage-lib-external-provisioner/v7 v7.0.2-0.20210722205635-f8e45184eca6

require (
	github.com/aws/aws-sdk-go v1.39.1
	k8s.io/api v0.21.2
	k8s.io/apimachinery v0.21.2
	k8s.io/client-go v0.21.2
	k8s.io/klog/v2 v2.9.0
	sigs.k8s.io/sig-storage-lib-external-provisioner/v7 v7.0.1
)
