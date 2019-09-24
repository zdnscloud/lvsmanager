package main

import (
	"github.com/gin-gonic/gin"

	"github.com/zdnscloud/cement/log"
	"github.com/zdnscloud/gok8s/cache"
	"github.com/zdnscloud/gok8s/client"
	"github.com/zdnscloud/gok8s/client/config"
	"github.com/zdnscloud/gorest/adaptor"
	"os"
	"strconv"

	"github.com/zdnscloud/cluster-agent/blockdevice"
	"github.com/zdnscloud/cluster-agent/configsyncer"
	"github.com/zdnscloud/cluster-agent/network"
	"github.com/zdnscloud/cluster-agent/nodeagent"
	"github.com/zdnscloud/cluster-agent/service"
	"github.com/zdnscloud/cluster-agent/storage"
)

var (
	Version = resttypes.APIVersion{
		Version: "v1",
		Group:   "agent.zcloud.cn",
	}
)

func createK8SClient() (cache.Cache, client.Client, error) {
	config, err := config.GetConfig()
	if err != nil {
		return nil, nil, err
	}

	c, err := cache.New(config, cache.Options{})
	if err != nil {
		return nil, nil, err
	}

	cli, err := client.New(config, client.Options{})
	if err != nil {
		return nil, nil, err
	}

	stop := make(chan struct{})
	go c.Start(stop)
	c.WaitForCacheSync(stop)

	return c, cli, nil
}

func main() {
	log.InitLogger("debug")

	cache, cli, err := createK8SClient()
	if err != nil {
		log.Fatalf("Create cache failed:%s", err.Error())
	}

	configsyncer.NewConfigSyncer(cli, cache)

	to := os.Getenv("CACHE_TIME")
	if to == "" {
		to = "60"
	}
	timeout, err := strconv.Atoi(to)
	if err != nil {
		timeout = int(60)
	}

	nodeAgentMgr := nodeagent.New()
	storageMgr, err := storage.New(cache, timeout, nodeAgentMgr)
	if err != nil {
		log.Fatalf("Create storage manager failed:%s", err.Error())
	}
	networkMgr, err := network.New(cache)
	if err != nil {
		log.Fatalf("Create network manager failed:%s", err.Error())
	}
	serviceMgr, err := service.New(cache)
	if err != nil {
		log.Fatalf("Create service manager failed:%s", err.Error())
	}
	blockDeviceMgr, err := blockdevice.New(timeout, nodeAgentMgr)
	if err != nil {
		log.Fatalf("Create nodeblocks manager failed:%s", err.Error())
	}

	schemas := resttypes.NewSchemas()
	storageMgr.RegisterSchemas(&Version, schemas)
	networkMgr.RegisterSchemas(&Version, schemas)
	serviceMgr.RegisterSchemas(&Version, schemas)
	nodeAgentMgr.RegisterSchemas(&Version, schemas)
	blockDeviceMgr.RegisterSchemas(&Version, schemas)

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	server := api.NewAPIServer()
	if err := server.AddSchemas(schemas); err != nil {
		log.Fatalf("add schemas failed:%s", err.Error())
	}
	server.Use(api.RestHandler)
	adaptor.RegisterHandler(router, server, server.Schemas.UrlMethods())

	addr := "0.0.0.0:8090"
	router.Run(addr)
}
