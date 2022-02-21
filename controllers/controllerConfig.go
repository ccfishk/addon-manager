package controllers

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/keikoproj/addon-manager/pkg/addon"
	addonv1versioned "github.com/keikoproj/addon-manager/pkg/client/clientset/versioned"
)

type Config struct {
	namespace       string
	addonworkers    int
	resourceworkers int
	wfworkers       int

	dynCli dynamic.Interface
	k8sCli kubernetes.Interface
	client.Client
	Scheme *runtime.Scheme

	Addoncli addonv1versioned.Interface
	//Addonlister  addonv1listers.AddonLister //addon lister
	VersionCache addon.VersionCacheClient
}

func NewConfig(namespace string, addonworkers, resourceworkers, wfworkers int,
	dynCli dynamic.Interface, k8s kubernetes.Interface,
	client client.Client, scheme *runtime.Scheme,
	addoncli addonv1versioned.Interface) *Config {
	return &Config{
		namespace:       namespace,
		addonworkers:    addonworkers,
		resourceworkers: resourceworkers,
		wfworkers:       wfworkers,
		dynCli:          dynCli,
		k8sCli:          k8s,
		Client:          client,
		Scheme:          scheme,
		Addoncli:        addoncli,
		VersionCache:    addon.NewAddonVersionCacheClient(),
	}
}
