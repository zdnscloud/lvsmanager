package storage

import (
	"github.com/zdnscloud/cluster-agent/storage/lvm"
	"github.com/zdnscloud/cluster-agent/storage/nfs"
	"github.com/zdnscloud/cluster-agent/storage/types"
	"github.com/zdnscloud/cluster-agent/storage/utils"
	"github.com/zdnscloud/gok8s/cache"
	"github.com/zdnscloud/gorest/api"
	resttypes "github.com/zdnscloud/gorest/types"
)

type Storage interface {
	GetType() string
	GetInfo(map[string]int64) types.Storage
}

type StorageManager struct {
	api.DefaultHandler

	storages []Storage
}

func New(c cache.Cache) (*StorageManager, error) {
	lvm, err := lvm.New(c)
	if err != nil {
		return nil, err
	}
	nfs, err := nfs.New(c)
	if err != nil {
		return nil, err
	}
	return &StorageManager{
		storages: []Storage{lvm, nfs},
	}, nil
}

func (m *StorageManager) RegisterSchemas(version *resttypes.APIVersion, schemas *resttypes.Schemas) {
	schemas.MustImportAndCustomize(version, types.Storage{}, m, types.SetStorageSchema)
}

func (m *StorageManager) Get(ctx *resttypes.Context) interface{} {
	cls := ctx.Object.GetID()
	mountpoints, _ := utils.GetAllPvUsedSize()
	for _, s := range m.storages {
		if s.GetType() == cls {
			return s.GetInfo(mountpoints)
		}
	}
	return nil
}

func (m *StorageManager) List(ctx *resttypes.Context) interface{} {
	var infos []types.Storage
	mountpoints, _ := utils.GetAllPvUsedSize()
	for _, s := range m.storages {
		infos = append(infos, s.GetInfo(mountpoints))
	}
	return infos
}
