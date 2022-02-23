package controllers

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"

	addonapiv1 "github.com/keikoproj/addon-manager/pkg/apis/addon"
	addonv1versioned "github.com/keikoproj/addon-manager/pkg/client/clientset/versioned"
	"github.com/keikoproj/addon-manager/pkg/utils"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	"github.com/keikoproj/addon-manager/pkg/common"

	wfclientset "github.com/argoproj/argo-workflows/v3/pkg/client/clientset/versioned"
	addoninternal "github.com/keikoproj/addon-manager/pkg/addon"
	batch_v1 "k8s.io/api/batch/v1"

	rbac_v1 "k8s.io/api/rbac/v1"
)

const maxRetries = 5

var serverStartTime time.Time

// Event indicate the informerEvent
type Event struct {
	key          string
	eventType    string
	namespace    string
	resourceType string
}

// Controller object
type Controller struct {
	logger             *logrus.Entry
	clientset          kubernetes.Interface
	queue              workqueue.RateLimitingInterface
	informer           cache.SharedIndexInformer
	wfinformer         cache.SharedIndexInformer
	nsinformer         cache.SharedIndexInformer
	deploymentinformer cache.SharedIndexInformer

	srvinformer                cache.SharedIndexInformer
	configMapinformer          cache.SharedIndexInformer
	clusterRoleinformer        cache.SharedIndexInformer
	clusterRoleBindingInformer cache.SharedIndexInformer
	jobinformer                cache.SharedIndexInformer
	srvAcntinformer            cache.SharedIndexInformer

	dynCli   dynamic.Interface
	addoncli addonv1versioned.Interface
	wfcli    wfclientset.Interface

	recorder record.EventRecorder

	namespace string
	scheme    *runtime.Scheme

	versionCache addoninternal.VersionCacheClient
}

// Start prepares watchers and run their controllers, then waits for process termination signals
func Start() {
	var kubeClient kubernetes.Interface
	var cfg *rest.Config
	cfg, err := rest.InClusterConfig()
	if err != nil {
		kubeClient = utils.GetClientOutOfCluster()
	} else {
		kubeClient = utils.GetClient()
	}

	dynCli, err := dynamic.NewForConfig(cfg)
	if err != nil {
		panic(err)
	}
	ctx := context.TODO()

	namespace := "addon-manager-system"
	addoncli := common.NewAddonClient(cfg)
	resource := schema.GroupVersionResource{
		Group:    addonapiv1.Group,
		Version:  "v1alpha1",
		Resource: addonapiv1.AddonPlural,
	}
	informer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options meta_v1.ListOptions) (runtime.Object, error) {
				return dynCli.Resource(resource).Namespace("addon-manager-system").List(ctx, options)
			},
			WatchFunc: func(options meta_v1.ListOptions) (watch.Interface, error) {
				return dynCli.Resource(resource).Namespace("addon-manager-system").Watch(ctx, options)
			},
		},
		&unstructured.Unstructured{},
		0, //Skip resync
		cache.Indexers{},
	)

	wfcli := common.NewWFClient(cfg)
	workflowResyncPeriod := 20 * time.Minute
	wfinformer := NewWorkflowInformer(dynCli, namespace, workflowResyncPeriod, cache.Indexers{}, tweakListOptions)

	// addon resources namespace informers
	nsinformer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options meta_v1.ListOptions) (runtime.Object, error) {
				return kubeClient.CoreV1().Namespaces().List(ctx, meta_v1.ListOptions{
					LabelSelector: ResourcetweakListOptions()})
			},
			WatchFunc: func(options meta_v1.ListOptions) (watch.Interface, error) {
				return kubeClient.CoreV1().Namespaces().Watch(ctx, meta_v1.ListOptions{
					LabelSelector: ResourcetweakListOptions()})
			},
		},
		&v1.Namespace{},
		0, //Skip resync
		cache.Indexers{},
	)

	deploymentinformer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options meta_v1.ListOptions) (runtime.Object, error) {
				return kubeClient.AppsV1().Deployments("").List(ctx, meta_v1.ListOptions{
					LabelSelector: ResourcetweakListOptions()})
			},
			WatchFunc: func(options meta_v1.ListOptions) (watch.Interface, error) {
				return kubeClient.AppsV1().Deployments("").Watch(ctx, meta_v1.ListOptions{
					LabelSelector: ResourcetweakListOptions()})
			},
		},
		&appsv1.Deployment{},
		0, //Skip resync
		cache.Indexers{},
	)

	srvinformer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options meta_v1.ListOptions) (runtime.Object, error) {
				return kubeClient.CoreV1().Services("").List(ctx, meta_v1.ListOptions{
					LabelSelector: ResourcetweakListOptions()})
			},
			WatchFunc: func(options meta_v1.ListOptions) (watch.Interface, error) {
				return kubeClient.CoreV1().Services("").Watch(ctx, meta_v1.ListOptions{
					LabelSelector: ResourcetweakListOptions()})
			},
		},
		&v1.Service{},
		0, //Skip resync
		cache.Indexers{},
	)

	configMapinformer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options meta_v1.ListOptions) (runtime.Object, error) {
				return kubeClient.CoreV1().ConfigMaps("").List(ctx, meta_v1.ListOptions{
					LabelSelector: ResourcetweakListOptions()})
			},
			WatchFunc: func(options meta_v1.ListOptions) (watch.Interface, error) {
				return kubeClient.CoreV1().ConfigMaps("").Watch(ctx, meta_v1.ListOptions{
					LabelSelector: ResourcetweakListOptions()})
			},
		},
		&v1.ConfigMap{},
		0, //Skip resync
		cache.Indexers{},
	)

	clusterRoleinformer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options meta_v1.ListOptions) (runtime.Object, error) {
				return kubeClient.RbacV1().ClusterRoles().List(ctx, meta_v1.ListOptions{
					LabelSelector: ResourcetweakListOptions()})
			},
			WatchFunc: func(options meta_v1.ListOptions) (watch.Interface, error) {
				return kubeClient.RbacV1().ClusterRoles().Watch(ctx, meta_v1.ListOptions{
					LabelSelector: ResourcetweakListOptions()})
			},
		},
		&rbac_v1.ClusterRole{},
		0, //Skip resync
		cache.Indexers{},
	)

	clusterRoleBindingInformer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options meta_v1.ListOptions) (runtime.Object, error) {
				return kubeClient.RbacV1().ClusterRoleBindings().List(ctx, meta_v1.ListOptions{
					LabelSelector: ResourcetweakListOptions()})
			},
			WatchFunc: func(options meta_v1.ListOptions) (watch.Interface, error) {
				return kubeClient.RbacV1().ClusterRoleBindings().Watch(ctx, meta_v1.ListOptions{
					LabelSelector: ResourcetweakListOptions()})
			},
		},
		&rbac_v1.ClusterRoleBinding{},
		0, //Skip resync
		cache.Indexers{},
	)

	srvAcntinformer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options meta_v1.ListOptions) (runtime.Object, error) {
				return kubeClient.CoreV1().ServiceAccounts(namespace).List(ctx, meta_v1.ListOptions{
					LabelSelector: ResourcetweakListOptions()})
			},
			WatchFunc: func(options meta_v1.ListOptions) (watch.Interface, error) {
				return kubeClient.CoreV1().ServiceAccounts(namespace).Watch(ctx, meta_v1.ListOptions{
					LabelSelector: ResourcetweakListOptions()})
			},
		},
		&v1.ServiceAccount{},
		0, //Skip resync
		cache.Indexers{},
	)

	jobinformer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options meta_v1.ListOptions) (runtime.Object, error) {
				return kubeClient.BatchV1().Jobs(namespace).List(ctx, meta_v1.ListOptions{
					LabelSelector: ResourcetweakListOptions()})
			},
			WatchFunc: func(options meta_v1.ListOptions) (watch.Interface, error) {
				return kubeClient.BatchV1().Jobs(namespace).Watch(ctx, meta_v1.ListOptions{
					LabelSelector: ResourcetweakListOptions()})
			},
		},
		&batch_v1.Job{},
		0, //Skip resync
		cache.Indexers{},
	)

	c := newResourceController(kubeClient, dynCli, addoncli, wfcli, informer, wfinformer, nsinformer, deploymentinformer,
		srvinformer, configMapinformer, clusterRoleinformer, clusterRoleBindingInformer, jobinformer, srvAcntinformer,
		"addon", "addon-manager-system")
	stopCh := make(chan struct{})
	defer close(stopCh)

	go c.Run(stopCh)

	sigterm := make(chan os.Signal, 1)
	signal.Notify(sigterm, syscall.SIGTERM)
	signal.Notify(sigterm, syscall.SIGINT)
	<-sigterm
}

func newResourceController(kubeClient kubernetes.Interface, dynCli dynamic.Interface, addoncli addonv1versioned.Interface, wfcli wfclientset.Interface, informer, wfinformer, nsinformer, deploymentinformer, srvinformer, configMapinformer, clusterRoleinformer, clusterRoleBindingInformer, jobinformer, srvAcntinformer cache.SharedIndexInformer, resourceType, namespace string) *Controller {
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	var newEvent Event
	var err error

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			newEvent.key, err = cache.MetaNamespaceKeyFunc(obj)
			newEvent.eventType = "create"

			logrus.WithField("controllers", "addon").Infof("Processing add to %v: %s", resourceType, newEvent.key)
			if err == nil {
				queue.Add(newEvent)
			}
		},
		UpdateFunc: func(old, new interface{}) {
			newEvent.key, err = cache.MetaNamespaceKeyFunc(old)
			newEvent.eventType = "update"

			logrus.WithField("controllers", "addon").Infof("Processing update to %v: %s", resourceType, newEvent.key)
			if err == nil {
				queue.Add(newEvent)
			}
		},
		DeleteFunc: func(obj interface{}) {
			newEvent.key, err = cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			newEvent.eventType = "delete"

			//newEvent.namespace = utils.GetObjectMetaData(obj).Namespace
			logrus.WithField("controllers", "addon-"+resourceType).Infof("Processing delete to %v: %s", resourceType, newEvent.key)
			if err == nil {
				queue.Add(newEvent)
			}
		},
	})

	return &Controller{
		logger:     logrus.WithField("controllers", "-"+resourceType),
		clientset:  kubeClient,
		dynCli:     dynCli,
		addoncli:   addoncli,
		wfcli:      wfcli,
		informer:   informer,
		wfinformer: wfinformer,
		nsinformer: nsinformer,

		srvAcntinformer:            srvAcntinformer,
		configMapinformer:          configMapinformer,
		clusterRoleinformer:        clusterRoleinformer,
		clusterRoleBindingInformer: clusterRoleBindingInformer,

		jobinformer: jobinformer,
		srvinformer: srvinformer,

		queue:              queue,
		deploymentinformer: deploymentinformer,
		namespace:          namespace,
	}
}

func (c *Controller) setupwfhandlers(ctx context.Context, wfinformer cache.SharedIndexInformer) {
	var newEvent Event
	var err error
	resourceType := "workflow"

	wfinformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			newEvent.key, err = cache.MetaNamespaceKeyFunc(obj)
			newEvent.eventType = "create"

			logrus.WithField("controllers", "wf").Infof("Processing add to %v: %s", resourceType, newEvent.key)
			c.handleWorkFlowAdd(ctx, obj)
		},
		UpdateFunc: func(old, new interface{}) {
			newEvent.key, err = cache.MetaNamespaceKeyFunc(old)
			newEvent.eventType = "update"

			logrus.WithField("controllers", "wf").Infof("Processing update to %v: %s", resourceType, newEvent.key)
			c.handleWorkFlowUpdate(ctx, new)

		},
		DeleteFunc: func(obj interface{}) {
			newEvent.key, err = cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			newEvent.eventType = "delete"
			c.handleWorkFlowDelete(ctx, newEvent)
		},
	})
	fmt.Printf("%v", err)
}

func (c *Controller) setupresourcehandlers(ctx context.Context, nsinformer, deploymentinformer cache.SharedIndexInformer) {
	var newEvent Event
	var err error
	resourceType := "namespace"

	nsinformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			newEvent.key, err = cache.MetaNamespaceKeyFunc(obj)

			logrus.WithField("controllers", "namespace").Infof("Processing add to %v: %s", resourceType, newEvent.key)
			c.handleNamespaceAdd(ctx, obj)

		},
		UpdateFunc: func(old, new interface{}) {
			newEvent.key, err = cache.MetaNamespaceKeyFunc(old)
			newEvent.eventType = "update"

			logrus.WithField("controllers", "namespace").Infof("Processing update to %v: %s", resourceType, newEvent.key)
			c.handleNamespaceUpdate(ctx, new)

		},
		DeleteFunc: func(obj interface{}) {
			newEvent.key, err = cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			newEvent.eventType = "delete"
			c.handleNamespaceDeletion(ctx, obj)
		},
	})

	resourceType = "deployment"
	deploymentinformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			newEvent.key, err = cache.MetaNamespaceKeyFunc(obj)
			newEvent.eventType = "create"

			logrus.WithField("controllers", "deployment").Infof("Processing add to %v: %s", resourceType, newEvent.key)
			c.handleDeploymentAdd(ctx, obj)

		},
		UpdateFunc: func(old, new interface{}) {
			newEvent.key, err = cache.MetaNamespaceKeyFunc(old)
			newEvent.eventType = "update"

			logrus.WithField("controllers", "deployment").Infof("Processing update to %v: %s", resourceType, newEvent.key)
			c.handleDeploymentUpdate(ctx, new)

		},
		DeleteFunc: func(obj interface{}) {
			newEvent.key, err = cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			newEvent.eventType = "delete"
			c.handleDeploymentDeletion(ctx, obj)
		},
	})

	resourceType = "ServiceAccount"
	c.srvAcntinformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			newEvent.key, err = cache.MetaNamespaceKeyFunc(obj)
			newEvent.eventType = "create"

			logrus.WithField("controllers", "ServiceAccount").Infof("Processing add to %v: %s", resourceType, newEvent.key)
			c.handleServiceAccountAdd(ctx, obj)

		},
		UpdateFunc: func(old, new interface{}) {
			newEvent.key, err = cache.MetaNamespaceKeyFunc(old)
			newEvent.eventType = "update"

			logrus.WithField("controllers", "ServiceAccount").Infof("Processing update to %v: %s", resourceType, newEvent.key)
			c.handleServiceAccountUpdate(ctx, new)

		},
		DeleteFunc: func(obj interface{}) {
			newEvent.key, err = cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			newEvent.eventType = "delete"
			c.handleServiceAccountDeletion(ctx, obj)
		},
	})

	resourceType = "ConfigMap"
	c.srvAcntinformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			newEvent.key, err = cache.MetaNamespaceKeyFunc(obj)
			newEvent.eventType = "create"

			logrus.WithField("controllers", "ConfigMap").Infof("Processing add to %v: %s", resourceType, newEvent.key)
			c.handleConfigMapAdd(ctx, obj)

		},
		UpdateFunc: func(old, new interface{}) {
			newEvent.key, err = cache.MetaNamespaceKeyFunc(old)
			newEvent.eventType = "update"

			logrus.WithField("controllers", "ConfigMap").Infof("Processing update to %v: %s", resourceType, newEvent.key)
			c.handleConfigMapUpdate(ctx, new)

		},
		DeleteFunc: func(obj interface{}) {
			newEvent.key, err = cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			newEvent.eventType = "delete"
			c.handleConfigMapDeletion(ctx, obj)
		},
	})

	resourceType = "ClusterRole"
	c.clusterRoleinformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			newEvent.key, err = cache.MetaNamespaceKeyFunc(obj)
			newEvent.eventType = "create"

			logrus.WithField("controllers", "ClusterRole").Infof("Processing add to %v: %s", resourceType, newEvent.key)
			c.handleClusterRoleAdd(ctx, obj)

		},
		UpdateFunc: func(old, new interface{}) {
			newEvent.key, err = cache.MetaNamespaceKeyFunc(old)
			newEvent.eventType = "update"

			logrus.WithField("controllers", "ClusterRole").Infof("Processing update to %v: %s", resourceType, newEvent.key)
			c.handleClusterRoleUpdate(ctx, new)

		},
		DeleteFunc: func(obj interface{}) {
			newEvent.key, err = cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			newEvent.eventType = "delete"
			c.handleClusterRoleDeletion(ctx, obj)
		},
	})

	resourceType = "ClusterRoleBinding"
	c.clusterRoleBindingInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			newEvent.key, err = cache.MetaNamespaceKeyFunc(obj)
			newEvent.eventType = "create"

			logrus.WithField("controllers", "ClusterRoleBinding").Infof("Processing add to %v: %s", resourceType, newEvent.key)
			c.handleClusterRoleBindingAdd(ctx, obj)

		},
		UpdateFunc: func(old, new interface{}) {
			newEvent.key, err = cache.MetaNamespaceKeyFunc(old)
			newEvent.eventType = "update"

			logrus.WithField("controllers", "ClusterRoleBinding").Infof("Processing update to %v: %s", resourceType, newEvent.key)
			c.handleClusterRoleBindingUpdate(ctx, new)

		},
		DeleteFunc: func(obj interface{}) {
			newEvent.key, err = cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			newEvent.eventType = "delete"
			c.handleClusterRoleBindingDeletion(ctx, obj)
		},
	})

	fmt.Print(err)
}

// Run starts the addon-controllers controller
func (c *Controller) Run(stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	c.logger.Info("Starting keiko addon-manager controller")
	ctx := context.TODO()
	serverStartTime = time.Now().Local()

	c.recorder = createEventRecorder(c.namespace, c.clientset, c.logger)
	c.scheme = common.GetAddonMgrScheme()
	c.versionCache = addoninternal.NewAddonVersionCacheClient()

	c.setupwfhandlers(ctx, c.wfinformer)
	c.setupresourcehandlers(ctx, c.nsinformer, c.deploymentinformer)

	go c.informer.Run(stopCh)
	go c.wfinformer.Run(stopCh)
	go c.nsinformer.Run(stopCh)
	go c.deploymentinformer.Run(stopCh)
	go c.srvAcntinformer.Run(stopCh)
	go c.configMapinformer.Run(stopCh)
	go c.clusterRoleinformer.Run(stopCh)
	go c.clusterRoleBindingInformer.Run(stopCh)
	go c.jobinformer.Run(stopCh)

	if !cache.WaitForCacheSync(stopCh, c.HasSynced, c.wfinformer.HasSynced) {
		utilruntime.HandleError(fmt.Errorf("Timed out waiting for caches to sync"))
		return
	}

	c.logger.Info("Keiko addon-manager controller synced and ready")

	wait.Until(c.runWorker, time.Second, stopCh)
}

// HasSynced is required for the cache.Controller interface.
func (c *Controller) HasSynced() bool {
	return c.informer.HasSynced()
}

// LastSyncResourceVersion is required for the cache.Controller interface.
func (c *Controller) LastSyncResourceVersion() string {
	return c.informer.LastSyncResourceVersion()
}

func (c *Controller) runWorker() {
	defer utilruntime.HandleCrash()

	ctx := context.Background()
	for c.processNextItem(ctx) {
		// continue looping
	}
}

func (c *Controller) processNextItem(ctx context.Context) bool {
	newEvent, quit := c.queue.Get()
	if quit {
		c.logger.Debugf("received shutdown message.")
		return false
	}
	defer c.queue.Done(newEvent)

	err := c.processItem(ctx, newEvent.(Event))
	if err == nil {
		c.queue.Forget(newEvent)
	} else if c.queue.NumRequeues(newEvent) < maxRetries {
		c.logger.Errorf("Error processing %s (will retry): %v", newEvent.(Event).key, err)
		c.queue.AddRateLimited(newEvent)
	} else {
		// err != nil and too many retries
		c.logger.Errorf("Error processing %s (giving up): %v", newEvent.(Event).key, err)
		c.queue.Forget(newEvent)
		utilruntime.HandleError(err)
	}

	return true
}

func (c *Controller) processItem(ctx context.Context, newEvent Event) error {
	obj, exists, err := c.informer.GetIndexer().GetByKey(newEvent.key)
	if err != nil {
		msg := fmt.Sprintf("failed fetching key %s from cache, err %v ", newEvent.key, err)
		c.logger.Error(msg)
		return fmt.Errorf(msg)
	} else if !exists {
		msg := fmt.Sprintf("obj %s does not exist", newEvent.key)
		c.logger.Error(msg)
		return fmt.Errorf(msg)
	}

	addon, err := common.FromUnstructured(obj.(*unstructured.Unstructured))
	if err != nil {
		msg := fmt.Sprintf("obj %s is not addon, err %v", newEvent.key, err)
		c.logger.Error(msg)
		return fmt.Errorf(msg)
	}

	// process events based on its type
	switch newEvent.eventType {
	case "create":
		return c.handleAddonCreation(ctx, addon)
	case "update":
		return c.handleAddonUpdate(ctx, addon)
	case "delete":
		return c.handleAddonDeletion(ctx, addon)
	}
	return nil
}
