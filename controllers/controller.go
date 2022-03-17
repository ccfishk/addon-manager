package controllers

import (
	"context"

	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/keikoproj/addon-manager/pkg/client/informers/externalversions/internalinterfaces"
)

type WfInformers struct {
	nsInformers dynamicinformer.DynamicSharedInformerFactory

	stopCh <-chan struct{}
}

func NewWfInformers(nsInfo dynamicinformer.DynamicSharedInformerFactory, stopCh <-chan struct{}) *WfInformers {
	return &WfInformers{
		nsInformers: nsInfo,
		stopCh:      stopCh,
	}
}

func (wfinfo *WfInformers) Start(ctx context.Context) error {
	s := wfinfo.stopCh
	wfinfo.nsInformers.Start(s)
	wfinfo.nsInformers.WaitForCacheSync(s)
	<-wfinfo.stopCh
	return nil
}

// api informers
func NewWorkflowInformer(dclient dynamic.Interface, ns string, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	resource := schema.GroupVersionResource{
		Group:    "argoproj.io",
		Version:  "v1alpha1",
		Resource: "workflows",
	}
	informer := NewFilteredUnstructuredInformer(
		resource,
		dclient,
		ns,
		resyncPeriod,
		indexers,
		tweakListOptions,
	)
	return informer
}

func NewFilteredUnstructuredInformer(resource schema.GroupVersionResource, client dynamic.Interface, namespace string, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	ctx := context.Background()
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.Resource(resource).Namespace(namespace).List(ctx, options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.Resource(resource).Namespace(namespace).Watch(ctx, options)
			},
		},
		&unstructured.Unstructured{},
		resyncPeriod,
		indexers,
	)
}

func TweakListOptions(options *metav1.ListOptions) {
}
