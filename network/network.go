package network

import (
	"context"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/zdnscloud/gok8s/cache"
	"github.com/zdnscloud/gok8s/controller"
	"github.com/zdnscloud/gok8s/event"
	"github.com/zdnscloud/gok8s/handler"
	"github.com/zdnscloud/gok8s/predicate"
	"github.com/zdnscloud/gorest"
	resttypes "github.com/zdnscloud/gorest/resource"
)

type NetworkManager struct {
	api.DefaultHandler
	networks *NetworkCache
	cache    cache.Cache
	lock     sync.RWMutex
	stopCh   chan struct{}
}

func New(c cache.Cache) (*NetworkManager, error) {
	ctrl := controller.New("networkCache", c, scheme.Scheme)
	ctrl.Watch(&corev1.Node{})
	ctrl.Watch(&corev1.Pod{})
	ctrl.Watch(&corev1.Service{})

	stopCh := make(chan struct{})
	m := &NetworkManager{
		stopCh: stopCh,
		cache:  c,
	}
	if err := m.initNetworkManagers(); err != nil {
		return nil, err
	}

	go ctrl.Start(stopCh, m, predicate.NewIgnoreUnchangedUpdate())
	return m, nil
}

func (m *NetworkManager) RegisterSchemas(version *resttypes.APIVersion, schemas *resttypes.Schemas) {
	schemas.MustImportAndCustomize(version, NodeNetwork{}, m, SetNodeNetworkSchema)
	schemas.MustImportAndCustomize(version, PodNetwork{}, m, SetPodNetworkSchema)
	schemas.MustImportAndCustomize(version, ServiceNetwork{}, m, SetServiceNetworkSchema)
}

func (m *NetworkManager) initNetworkManagers() error {
	nodes := &corev1.NodeList{}
	err := m.cache.List(context.TODO(), nil, nodes)
	if err != nil {
		return err
	}

	nc := newNetworkCache()
	for _, node := range nodes.Items {
		nc.OnNewNode(&node)
	}

	m.networks = nc
	return nil
}

func (m *NetworkManager) List(ctx *resttypes.Context) interface{} {
	m.lock.RLock()
	defer m.lock.RUnlock()
	switch ctx.Object.GetType() {
	case NodeNetworkType:
		return m.networks.GetNodeNetworks()
	case PodNetworkType:
		return m.networks.GetPodNetworks()
	case ServiceNetworkType:
		return m.networks.GetServiceNetworks()
	default:
		return nil
	}
}

func (m *NetworkManager) OnCreate(e event.CreateEvent) (handler.Result, error) {
	m.lock.Lock()
	defer m.lock.Unlock()

	switch obj := e.Object.(type) {
	case *corev1.Node:
		m.networks.OnNewNode(obj)
	case *corev1.Pod:
		m.networks.OnNewPod(obj)
	case *corev1.Service:
		m.networks.OnNewService(obj)
	}

	return handler.Result{}, nil
}

func (m *NetworkManager) OnUpdate(e event.UpdateEvent) (handler.Result, error) {
	m.lock.Lock()
	defer m.lock.Unlock()

	switch newObj := e.ObjectNew.(type) {
	case *corev1.Service:
		if e.ObjectOld.(*corev1.Service).Spec.ClusterIP != newObj.Spec.ClusterIP {
			m.networks.OnUpdateService(newObj)
		}
	case *corev1.Pod:
		m.networks.OnUpdatePod(e.ObjectOld.(*corev1.Pod), newObj)
	}

	return handler.Result{}, nil
}

func (m *NetworkManager) OnDelete(e event.DeleteEvent) (handler.Result, error) {
	m.lock.Lock()
	defer m.lock.Unlock()

	switch obj := e.Object.(type) {
	case *corev1.Node:
		m.networks.OnDeleteNode(obj)
	case *corev1.Pod:
		m.networks.OnDeletePod(obj)
	case *corev1.Service:
		m.networks.OnDeleteService(obj)
	}

	return handler.Result{}, nil
}

func (m *NetworkManager) OnGeneric(e event.GenericEvent) (handler.Result, error) {
	return handler.Result{}, nil
}
