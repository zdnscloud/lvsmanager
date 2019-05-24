package service

import (
	"context"
	"fmt"
	"sync"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	extv1beta1 "k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"

	"github.com/zdnscloud/cement/log"
	"github.com/zdnscloud/cement/set"
	"github.com/zdnscloud/gok8s/cache"
	"github.com/zdnscloud/gok8s/client"
	"github.com/zdnscloud/gok8s/helper"
)

const (
	AnnkeyForUDPIngress = "zcloud_ingress_udp"
	RunningState        = "Running"
)

type Service struct {
	name      string
	ingress   set.StringSet
	workloads []Workload
}

type ServiceMonitor struct {
	services  map[string]*Service
	ings      map[string]*Ingress
	workloads map[string]map[string]*Workload
	lock      sync.RWMutex

	cache cache.Cache
}

func newServiceMonitor(cache cache.Cache) *ServiceMonitor {
	return &ServiceMonitor{
		cache:     cache,
		services:  make(map[string]*Service),
		ings:      make(map[string]*Ingress),
		workloads: make(map[string]map[string]*Workload),
	}
}

func (s *ServiceMonitor) GetInnerServices() []InnerService {
	s.lock.RLock()
	defer s.lock.RUnlock()

	svcs := make([]InnerService, 0, len(s.services))
	for _, svc := range s.services {
		if len(svc.ingress) == 0 {
			svcs = append(svcs, InnerService{
				Name:      svc.name,
				Workloads: s.getLinkedWorkloads(svc),
			})
		}
	}
	return svcs
}

func (s *ServiceMonitor) GetOuterServices() []OuterService {
	s.lock.RLock()
	defer s.lock.RUnlock()
	outerSvcs := make([]OuterService, 0, len(s.services))
	//handle several services shared same ingress
	returnedIngress := set.NewStringSet()
	for _, svc := range s.services {
		for ing := range svc.ingress {
			if returnedIngress.Member(ing) == false {
				outerSvcs = append(outerSvcs, s.toOuterService(s.ings[ing])...)
				returnedIngress.Add(ing)
			}
		}
	}
	return outerSvcs
}

func (s *ServiceMonitor) toOuterService(ing *Ingress) []OuterService {
	outerSvcs := make([]OuterService, 0, len(ing.rules))
	var outerSvc OuterService
	for _, rule := range ing.rules {
		if rule.protocol == IngressProtocolHTTP {
			outerSvc.EntryPoint = fmt.Sprintf("%s://%s", rule.protocol, rule.host)
		} else {
			outerSvc.EntryPoint = fmt.Sprintf("%s:%d", rule.protocol, rule.port)
		}
		innerSvcs := make(map[string]InnerService)
		for _, p := range rule.paths {
			svc, ok := s.services[p.serviceName]
			if ok {
				innerSvcs[p.path] = InnerService{
					Name:      svc.name,
					Workloads: s.getLinkedWorkloads(svc),
				}
			}
		}
		outerSvc.Services = innerSvcs
		outerSvcs = append(outerSvcs, outerSvc)
	}
	return outerSvcs
}

func (s *ServiceMonitor) getLinkedWorkloads(svc *Service) []*Workload {
	var wls []*Workload
	for _, wl := range svc.workloads {
		if wlp := s.getWorkload(wl.Kind, wl.Name); wlp != nil {
			wls = append(wls, wlp)
		}
	}
	return wls
}

func (s *ServiceMonitor) OnNewService(k8ssvc *corev1.Service) {
	s.lock.Lock()
	defer s.lock.Unlock()

	svc, err := s.k8ssvcToSCService(k8ssvc)
	if err != nil {
		return
	}

	s.services[svc.name] = svc
	for name, ing := range s.ings {
		ss := ingressLinkedServices(ing)
		if ss.Member(svc.name) {
			s.linkIngressToService(name, svc.name)
		}
	}
}

func (s *ServiceMonitor) k8ssvcToSCService(k8ssvc *corev1.Service) (*Service, error) {
	svc := &Service{
		name: k8ssvc.Name,
	}

	if len(k8ssvc.Spec.Selector) == 0 {
		return svc, nil
	}

	ls := metav1.LabelSelector{
		MatchLabels: k8ssvc.Spec.Selector,
	}
	k8spods := corev1.PodList{}
	opts := &client.ListOptions{Namespace: k8ssvc.Namespace}
	labels, _ := metav1.LabelSelectorAsSelector(&ls)
	opts.LabelSelector = labels
	err := s.cache.List(context.TODO(), opts, &k8spods)
	if err != nil {
		log.Warnf("get pod list failed:%s", err.Error())
		return nil, err
	}

	workerLoads := make(map[string]Workload)
	for _, k8spod := range k8spods.Items {
		pod := Pod{
			Name:  k8spod.Name,
			State: helper.GetPodState(&k8spod),
		}

		name, kind, succeed := s.getPodOwner(&k8spod)
		if succeed == false {
			continue
		}

		wl := s.getWorkload(kind, name)
		if wl == nil {
			wl = &Workload{
				Name: name,
				Kind: kind,
			}
			s.addWorkload(wl)
		}
		wl.Pods = append(wl.Pods, pod)

		if _, ok := workerLoads[wl.Name]; ok == false {
			workerLoads[wl.Name] = Workload{
				Name: wl.Name,
				Kind: wl.Kind,
			}
		}
	}

	for _, wl := range workerLoads {
		svc.workloads = append(svc.workloads, wl)
	}
	svc.ingress = set.NewStringSet()
	return svc, nil
}

func (s *ServiceMonitor) getPodOwner(k8spod *corev1.Pod) (string, string, bool) {
	if len(k8spod.OwnerReferences) != 1 {
		return "", "", false
	}

	owner := k8spod.OwnerReferences[0]
	if owner.Kind != "ReplicaSet" {
		return owner.Name, owner.Kind, true
	}

	var k8srs appsv1.ReplicaSet
	err := s.cache.Get(context.TODO(), k8stypes.NamespacedName{k8spod.Namespace, owner.Name}, &k8srs)
	if err != nil {
		log.Warnf("get replicaset failed:%s", err.Error())
		return "", "", false
	}

	if len(k8srs.OwnerReferences) != 1 {
		log.Warnf("replicaset OwnerReferences is strange:%v", k8srs.OwnerReferences)
		return "", "", false
	}

	owner = k8srs.OwnerReferences[0]
	if owner.Kind != "Deployment" {
		log.Warnf("replicaset parent is not deployment but %v", owner.Kind)
		return "", "", false
	}
	return owner.Name, owner.Kind, true
}

func (s *ServiceMonitor) OnDeleteService(k8ssvc *corev1.Service) {
	s.lock.Lock()
	defer s.lock.Unlock()

	delete(s.services, k8ssvc.Name)
}

func (s *ServiceMonitor) OnUpdateService(oldk8ssvc, newk8ssvc *corev1.Service) {
	if isMapEqual(oldk8ssvc.Spec.Selector, newk8ssvc.Spec.Selector) {
		return
	}
	s.OnNewService(newk8ssvc)
}

func (s *ServiceMonitor) OnUpdatePod(oldk8spod, newk8spod *corev1.Pod) {
	oldState := helper.GetPodState(oldk8spod)
	newState := helper.GetPodState(newk8spod)
	if newState == oldState {
		return
	}

	s.OnNewPod(newk8spod)
}

func (s *ServiceMonitor) OnNewPod(k8spod *corev1.Pod) {
	name, kind, succeed := s.getPodOwner(k8spod)
	if succeed == false {
		return
	}

	s.lock.Lock()
	defer s.lock.Unlock()
	wl := s.getWorkload(kind, name)
	if wl == nil && s.hasServiceLinktoWorkload(kind, name) {
		wl = &Workload{
			Name: name,
			Kind: kind,
		}
		s.addWorkload(wl)
	}

	if wl != nil {
		s.addPodToWorkload(k8spod, wl)
	}
}

func (s *ServiceMonitor) hasServiceLinktoWorkload(kind, name string) bool {
	for _, svc := range s.services {
		for _, wl := range svc.workloads {
			if wl.Kind == kind && wl.Name == name {
				return true
			}
		}
	}
	return false
}

func (s *ServiceMonitor) getWorkload(kind, name string) *Workload {
	wls, ok := s.workloads[kind]
	if ok == false {
		return nil
	}
	wl, ok := wls[name]
	if ok == false {
		return nil
	}
	return wl
}

func (s *ServiceMonitor) addWorkload(wl *Workload) {
	wls, ok := s.workloads[wl.Kind]
	if ok == false {
		wls = make(map[string]*Workload)
		s.workloads[wl.Kind] = wls
	}
	wls[wl.Name] = wl
}

func (s *ServiceMonitor) deleteWorkload(wl *Workload) {
	wls, ok := s.workloads[wl.Kind]
	if ok {
		delete(wls, wl.Name)
	}
}

func (s *ServiceMonitor) addPodToWorkload(k8spod *corev1.Pod, wl *Workload) {
	pod := Pod{
		Name:  k8spod.Name,
		State: helper.GetPodState(k8spod),
	}
	for i, p := range wl.Pods {
		if p.Name == pod.Name {
			wl.Pods[i] = pod
			return
		}
	}
	wl.Pods = append(wl.Pods, pod)
}

func (s *ServiceMonitor) OnDeletePod(k8spod *corev1.Pod) {
	name, kind, succeed := s.getPodOwner(k8spod)
	if succeed == false {
		return
	}

	s.lock.Lock()
	defer s.lock.Unlock()
	wl := s.getWorkload(kind, name)
	if wl != nil {
		s.removePodFromWorkload(k8spod.Name, wl)
	}
}

func (s *ServiceMonitor) removePodFromWorkload(podName string, wl *Workload) {
	for i, pod := range wl.Pods {
		if pod.Name == podName {
			wl.Pods = append(wl.Pods[:i], wl.Pods[i+1:]...)
			if len(wl.Pods) == 0 {
				s.deleteWorkload(wl)
			}
			break
		}
	}
}

func (s *ServiceMonitor) OnNewIngress(k8sing *extv1beta1.Ingress) {
	ing := k8sIngressToSCIngress(k8sing)
	s.lock.Lock()
	defer s.lock.Unlock()
	s.addIngress(ing)
}

func (s *ServiceMonitor) addIngress(ing *Ingress) {
	old, ok := s.ings[ing.name]
	involedServices := ingressLinkedServices(ing)
	if ok {
		oldServices := ingressLinkedServices(old)
		old.rules = append(old.rules, ing.rules...)
		involedServices = involedServices.Difference(oldServices)
	} else {
		s.ings[ing.name] = ing
	}

	for service := range involedServices {
		s.linkIngressToService(ing.name, service)
	}
}

func (s *ServiceMonitor) OnNewTransportLayerIngress(ing *Ingress) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.addIngress(ing)
}

func (s *ServiceMonitor) OnReplaceTransportLayerIngress(oldIng, newIng *Ingress) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.updateIngress(oldIng, newIng)
}

func (s *ServiceMonitor) linkIngressToService(ing, service string) {
	svc, ok := s.services[service]
	if ok == false {
		log.Warnf("unknown service %s specified in ingress %s", service, ing)
	} else {
		svc.ingress.Add(ing)
	}
}

func (s *ServiceMonitor) removeIngressFromService(ing, service string) {
	svc, ok := s.services[service]
	if ok == false {
		log.Warnf("unknown service %s specified in ingress %s", service, ing)
	} else {
		svc.ingress.Remove(ing)
	}
}

func k8sIngressToSCIngress(k8sing *extv1beta1.Ingress) *Ingress {
	ing := &Ingress{
		name: k8sing.Name,
	}
	k8srules := k8sing.Spec.Rules
	var rules []IngressRule
	for _, rule := range k8srules {
		http := rule.HTTP
		if http == nil {
			continue
		}

		var paths []IngressPath
		for _, p := range http.Paths {
			paths = append(paths, IngressPath{
				serviceName: p.Backend.ServiceName,
				path:        p.Path,
			})
		}

		rules = append(rules, IngressRule{
			host:     rule.Host,
			paths:    paths,
			protocol: IngressProtocolHTTP,
		})
	}

	ing.rules = rules
	return ing
}

func (s *ServiceMonitor) OnUpdateIngress(oldk8sing, newk8sing *extv1beta1.Ingress) {
	oldIng := k8sIngressToSCIngress(oldk8sing)
	newIng := k8sIngressToSCIngress(newk8sing)

	s.lock.Lock()
	defer s.lock.Unlock()
	s.updateIngress(oldIng, newIng)
}

//either update http ingress or update udp/tcp ingress
//update partial ingress in http or in udp/tcp will cause data corruption
func (s *ServiceMonitor) updateIngress(oldIng, newIng *Ingress) {
	oldIngInMem, ok := s.ings[oldIng.name]
	if ok == false {
		log.Errorf("update unknown ingress %s", oldIng.name)
		return
	}

	if len(oldIngInMem.rules) == 0 {
		log.Errorf("update ingress with empty rule %s", oldIng.name)
		return
	}

	oldServices := ingressLinkedServices(oldIngInMem)
	ingressRemoveRules(oldIngInMem, oldIng.rules[0].protocol)
	if newIng != nil {
		oldIngInMem.rules = append(oldIngInMem.rules, newIng.rules...)
	}
	newServices := ingressLinkedServices(oldIngInMem)
	for service := range oldServices.Difference(newServices) {
		s.removeIngressFromService(oldIng.name, service)
	}
	for service := range newServices.Difference(oldServices) {
		s.linkIngressToService(oldIng.name, service)
	}

	if len(oldIngInMem.rules) == 0 {
		delete(s.ings, oldIng.name)
	}
}

func (s *ServiceMonitor) OnDeleteIngress(k8sing *extv1beta1.Ingress) {
	ing := k8sIngressToSCIngress(k8sing)
	s.lock.Lock()
	defer s.lock.Unlock()

	s.updateIngress(ing, nil)
}

func (s *ServiceMonitor) OnDeleteTransportLayerIngress(ing *Ingress) {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.updateIngress(ing, nil)
}

func isMapEqual(m1, m2 map[string]string) bool {
	if len(m1) != len(m2) {
		return false
	}

	for k, v1 := range m1 {
		v2, ok := m2[k]
		if ok == false || v1 != v2 {
			return false
		}
	}
	return true
}
