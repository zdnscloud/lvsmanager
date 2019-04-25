package storageclass

import (
	"github.com/zdnscloud/cluster-agent/storage/types"
)

const (
	CtrlName         = "storagecontroller"
	ZKENFSPvcName    = "nfs-data-nfs-provisioner-0"
	CSIDefaultVgName = "k8s"
)

type StorageCache struct {
	Name      string                 `json:"name,omitempty"`
	Nodes     []types.Node           `json:"nodes,omitempty"`
	PVs       []types.PV             `json:"pvs,omitempty"`
	PvAndPvc  map[string]PVC         `json:"_"`
	PvcAndPod map[string][]types.Pod `json:"_"`
}

type PVC struct {
	Name       string
	VolumeName string
	Pods       []types.Pod
}
