package controllers

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"syscall"
	"time"

	wfv1 "github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/controller-runtime/pkg/client"

	addonapiv1 "github.com/keikoproj/addon-manager/pkg/apis/addon"
	addonv1 "github.com/keikoproj/addon-manager/pkg/apis/addon/v1alpha1"
	addonv1versioned "github.com/keikoproj/addon-manager/pkg/client/clientset/versioned"
	"github.com/keikoproj/addon-manager/pkg/common"
	"github.com/keikoproj/addon-manager/pkg/workflows"
	addonwfutility "github.com/keikoproj/addon-manager/pkg/workflows"

	"github.com/keikoproj/addon-manager/pkg/addon"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// ResourceController object
type ResourceController struct {
	logger     *logrus.Entry
	clientset  kubernetes.Interface
	queue      workqueue.RateLimitingInterface
	informer   cache.SharedIndexInformer
	wfinformer cache.SharedIndexInformer
	dynCli     dynamic.Interface
	//eventHandler handlers.Handler

	Recorder      record.EventRecorder
	client.Client // for workflow
	Scheme        *runtime.Scheme

	addoncli     addonv1versioned.Interface
	versionCache addon.VersionCacheClient
}

// Start prepares watchers and run their controllers, then waits for process termination signals
func StartAddonController(ctx context.Context, config *Config) {

	c := &ResourceController{}
	c.logger = logrus.WithField("controllers", "watch-addon-workflow-resources")
	c.queue = workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	addresource := schema.GroupVersionResource{
		Group:    addonapiv1.Group,
		Version:  "v1alpha1",
		Resource: addonapiv1.AddonPlural,
	}
	c.informer = cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				return config.dynCli.Resource(addresource).Namespace(config.namespace).List(ctx, options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				return config.dynCli.Resource(addresource).Namespace(config.namespace).Watch(ctx, options)
			},
		},
		&unstructured.Unstructured{},
		0, //Skip resync
		cache.Indexers{},
	)

	wfresource := schema.GroupVersionResource{
		Group:    "argoproj.io",
		Version:  "v1alpha1",
		Resource: "workflows",
	}
	c.wfinformer = cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				return config.dynCli.Resource(wfresource).Namespace(config.namespace).List(ctx, options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				return config.dynCli.Resource(wfresource).Namespace(config.namespace).Watch(ctx, options)
			},
		},
		&unstructured.Unstructured{},
		0, //Skip resync
		cache.Indexers{},
	)

	c.Recorder = CreateEventRecorder(config.namespace, config.k8sCli, c.logger)
	c.Scheme = config.Scheme
	c.Client = config.Client
	c.dynCli = config.dynCli
	c.addoncli = config.Addoncli
	c.versionCache = config.VersionCache

	c.setupAddonInformerHandler(c.informer, c.queue, "addon")
	c.setupWorkflowHandler(ctx, c.wfinformer)

	stopCh := make(chan struct{})
	defer close(stopCh)

	go c.run(ctx, stopCh, config.addonworkers)

	sigterm := make(chan os.Signal, 1)
	signal.Notify(sigterm, syscall.SIGTERM)
	signal.Notify(sigterm, syscall.SIGINT)
	<-sigterm
}

func (c *ResourceController) setupWorkflowHandler(ctx context.Context, wfsharedInforms cache.SharedIndexInformer) {
	wfsharedInforms.AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			UpdateFunc: func(oldObj, newObj interface{}) {
				c.handleWorkFlowUpdate(ctx, newObj)
			},

			AddFunc: func(obj interface{}) {
				c.handleWorkFlowAdd(ctx, obj)
			},
		},
	)
}

func (c *ResourceController) setupAddonInformerHandler(informer cache.SharedIndexInformer, queue workqueue.RateLimitingInterface, resourceType string) {
	var newEvent Event
	var err error

	informer.AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			yes := !UnstructuredHasCompletedLabel(obj)
			fmt.Printf("\n not complete ? %v \n", yes)
			return yes
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				newEvent.key, err = cache.MetaNamespaceKeyFunc(obj)
				newEvent.eventType = "create"

				logrus.WithField("pkg", "resource-addon").Infof("Processing add to %v: %s", resourceType, newEvent.key)
				if err == nil {
					queue.Add(newEvent)
				}
			},
			UpdateFunc: func(old, new interface{}) {
				newEvent.key, err = cache.MetaNamespaceKeyFunc(old)
				newEvent.eventType = "update"

				logrus.WithField("pkg", "resource-addon").Infof("Processing update to %v: %s", resourceType, newEvent.key)
				if err == nil {
					queue.Add(newEvent)
				}
			},
			DeleteFunc: func(obj interface{}) {
				newEvent.key, err = cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
				newEvent.eventType = "delete"

				logrus.WithField("pkg", "resource-"+resourceType).Infof("Processing non-completed delete to %v: %s", resourceType, newEvent.key)
				if err == nil {
					queue.Add(newEvent)
				}
			},
		},
	})
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj interface{}) {
			addon, ok := obj.(*unstructured.Unstructured)
			if ok {
				newEvent.key, err = cache.DeletionHandlingMetaNamespaceKeyFunc(addon)
				newEvent.eventType = "delete"
				logrus.WithField("pkg", "resource-"+resourceType).Infof("Processing completed resource delete to %v: %s", resourceType, newEvent.key)
				if err == nil {
					queue.Add(newEvent)
				}

			}
		},
	})
}

// Run starts the resource controller
func (c *ResourceController) run(ctx context.Context, stopCh <-chan struct{}, workers int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	c.logger.Info("Starting resource controller")
	serverStartTime = time.Now().Local()

	go c.informer.Run(stopCh)
	go c.wfinformer.Run(stopCh)

	if !cache.WaitForCacheSync(stopCh, c.HasSynced) {
		utilruntime.HandleError(fmt.Errorf("timed out waiting for caches to sync"))
		return
	}

	c.logger.Info("Resource controller synced and ready")

	// fix me, even though, my performance is better
	wait.Until(c.runWorker, time.Second, stopCh)
}

// HasSynced is required for the cache.ResourceController interface.
func (c *ResourceController) HasSynced() bool {
	return c.informer.HasSynced()
}

// LastSyncResourceVersion is required for the cache.ResourceController interface.
func (c *ResourceController) LastSyncResourceVersion() string {
	return c.informer.LastSyncResourceVersion()
}

func (c *ResourceController) runWorker() {
	for c.processNextItem() {
		// continue looping
	}
}

func (c *ResourceController) processNextItem() bool {
	newEvent, quit := c.queue.Get()

	if quit {
		return false
	}
	defer c.queue.Done(newEvent)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := c.processItem(ctx, newEvent.(Event))
	if err == nil {
		// No error, reset the ratelimit counters
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

func (c *ResourceController) processItem(ctx context.Context, newEvent Event) error {
	obj, exists, err := c.informer.GetIndexer().GetByKey(newEvent.key)
	if err != nil {
		msg := fmt.Sprintf("failed fetching key %s from cache, err %v ", newEvent.key, err)
		c.logger.Error(msg)
		return fmt.Errorf(msg)
	} else if !exists {
		msg := fmt.Sprintf("\n obj %s does not exist \n", newEvent.key)
		fmt.Print(msg)
		return fmt.Errorf(msg)
	}

	addon, err := FromUnstructured(obj.(*unstructured.Unstructured))
	if err != nil {
		msg := fmt.Sprintf("obj %s is not addon, err %v", newEvent.key, err)
		fmt.Print(msg)
		return fmt.Errorf(msg)
	}

	switch newEvent.eventType {
	case "create":
		fmt.Printf("yes, add event ")
		return c.handleCreation(ctx, addon)
	case "update":
		fmt.Printf("yes, update event ")
		c.handleUpdate(ctx, addon)
	case "delete":
		fmt.Printf("yes, delete event ")
		c.handleDeletion(ctx, addon)
		return nil
	}
	return nil
}

func (c *ResourceController) handleCreation(ctx context.Context, instance *addonv1.Addon) error {
	c.logger.Info("yes, creating addon %s/%s", instance.Namespace, instance.Name)

	var wfl = workflows.NewWorkflowLifecycle(c.Client, c.dynCli, instance, c.Scheme, c.Recorder)

	procErr := c.createAddon(ctx, instance, wfl)
	if procErr != nil {
		return procErr
	}

	c.addAddonToCache(instance)

	return nil
}

func (c *ResourceController) handleUpdate(ctx context.Context, addon *addonv1.Addon) error {
	fmt.Printf("\n yes, update addon %s/%s\n", addon.Namespace, addon.Name)

	// // Check if addon installation expired.
	// if instance.Status.Lifecycle.Installed == addonv1.Pending && common.IsExpired(instance.Status.StartTime, TTL.Milliseconds()) {
	// 	reason := fmt.Sprintf("Addon %s/%s ttl expired, starttime exceeded %s", instance.Namespace, instance.Name, TTL.String())
	// 	c.Recorder.Event(instance, "Warning", "Failed", reason)
	// 	err := fmt.Errorf(reason)
	// 	c.logger.Error(err, reason)

	// 	instance.Status.Lifecycle.Installed = addonv1.Failed
	// 	instance.Status.Reason = reason

	// 	return err
	// }

	// // Validate Addon
	// if ok, err := addon.NewAddonValidator(instance, c.VersionCache, c.DynClient).Validate(); !ok {
	// 	// if an addons dependency is in a Pending state then make the parent addon Pending
	// 	if err != nil && strings.HasPrefix(err.Error(), addon.ErrDepPending) {
	// 		reason := fmt.Sprintf("Addon %s/%s is waiting on dependencies to be out of Pending state.", instance.Namespace, instance.Name)
	// 		// Record an event if addon is not valid
	// 		c.Recorder.Event(instance, "Normal", "Pending", reason)
	// 		instance.Status.Lifecycle.Installed = addonv1.Pending
	// 		instance.Status.Reason = reason

	// 		c.logger.Info(reason)

	// 		// requeue after 10 seconds
	// 		return nil
	// 	} else if err != nil && strings.HasPrefix(err.Error(), addon.ErrDepNotInstalled) {
	// 		reason := fmt.Sprintf("Addon %s/%s is waiting on dependencies to be installed. %v", instance.Namespace, instance.Name, err)
	// 		// Record an event if addon is not valid
	// 		c.Recorder.Event(instance, "Normal", "Failed", reason)
	// 		instance.Status.Lifecycle.Installed = addonv1.ValidationFailed
	// 		instance.Status.Reason = reason

	// 		c.logger.Info(reason)

	// 		// requeue after 30 seconds
	// 		return nil
	// 	}

	// 	reason := fmt.Sprintf("Addon %s/%s is not valid. %v", instance.Namespace, instance.Name, err)
	// 	// Record an event if addon is not valid
	// 	c.Recorder.Event(instance, "Warning", "Failed", reason)
	// 	instance.Status.Lifecycle.Installed = addonv1.ValidationFailed
	// 	instance.Status.Reason = reason

	// 	c.logger.Error(err, "Failed to validate addon.")

	// 	return err
	// }

	// // Execute PreReq and Install workflow, if spec body has changed.
	// // In the case when validation failed and continued here we should execute.
	// // Also if workflow is in Pending state, execute it to update status to terminal state.
	// if changedStatus || instance.Status.Lifecycle.Installed == addonv1.ValidationFailed ||
	// 	instance.Status.Lifecycle.Prereqs == addonv1.Pending || instance.Status.Lifecycle.Installed == addonv1.Pending {
	// 	c.logger.Info("Addon spec is updated, workflows will be generated")

	// 	err := c.executePrereqAndInstall(ctx, instance, wfl)
	// 	if err != nil {
	// 		return err
	// 	}
	// }

	return nil
}
func (c *ResourceController) handleDeletion(ctx context.Context, addon *addonv1.Addon) error {
	fmt.Printf("\n yes, delete addon %s/%s\n", addon.Namespace, addon.Name)
	return nil
}

func (c *ResourceController) handleWorkFlowUpdate(ctx context.Context, obj interface{}) error {
	fmt.Printf("\n yes, detect wf update \n")

	if err := addonwfutility.IsValidV1WorkFlow(obj); err != nil {
		msg := fmt.Sprintf("\n### error not an expected workflow object %#v", err)
		c.logger.Info(msg)
		return fmt.Errorf(msg)
	}
	wfobj := &wfv1.Workflow{}
	_ = runtime.DefaultUnstructuredConverter.FromUnstructured(obj.(*unstructured.Unstructured).UnstructuredContent(), wfobj)

	// check the associated addons and update its status
	msg := fmt.Sprintf("workflow %s/%s update status.phase %s", wfobj.GetNamespace(), wfobj.GetName(), wfobj.Status.Phase)
	c.logger.Info(msg)

	if len(string(wfobj.Status.Phase)) == 0 {
		msg := fmt.Sprintf("skip workflow ", wfobj.GetNamespace(), "/", wfobj.GetName(), "empty status update.")
		fmt.Print(msg)
		return nil
	}

	// find the Addon from the namespace and update its status accordingly
	addonName, lifecycle, err := addonwfutility.ExtractAddOnNameAndLifecycleStep(wfobj.GetName())
	if err != nil {
		msg := fmt.Sprintf("could not extract addon/lifecycle from %s/%s workflow.",
			wfobj.GetNamespace(),
			wfobj.GetName())
		c.logger.Info(msg)
		return fmt.Errorf(msg)
	}

	err = c.updateAddonStatus(wfobj.GetNamespace(), addonName, lifecycle, wfobj.Status.Phase)
	if err != nil {
		msg := fmt.Sprintf("\n### error failed updating %s status err %#v\n", addonName, err)
		c.logger.Info(msg)
		return fmt.Errorf(msg)
	}

	return nil
}

func (c *ResourceController) handleWorkFlowAdd(ctx context.Context, obj interface{}) error {
	fmt.Printf("\n yes, detect wf add \n")

	if err := addonwfutility.IsValidV1WorkFlow(obj); err != nil {
		msg := fmt.Sprintf("\n### error not an expected workflow object %#v", err)
		c.logger.Info(msg)
		return fmt.Errorf(msg)
	}
	wfobj := &wfv1.Workflow{}
	_ = runtime.DefaultUnstructuredConverter.FromUnstructured(obj.(*unstructured.Unstructured).UnstructuredContent(), wfobj)

	// check the associated addons and update its status
	msg := fmt.Sprintf("workflow %s/%s update status.phase %s", wfobj.GetNamespace(), wfobj.GetName(), wfobj.Status.Phase)
	c.logger.Info(msg)
	if len(string(wfobj.Status.Phase)) == 0 {
		msg := fmt.Sprintf("skip %s/%s workflow empty status.", wfobj.GetNamespace(),
			wfobj.GetName())
		c.logger.Info(msg)
		return nil
	}
	addonName, lifecycle, err := addonwfutility.ExtractAddOnNameAndLifecycleStep(wfobj.GetName())
	if err != nil {
		msg := fmt.Sprintf("could not extract addon/lifecycle from %s/%s workflow.",
			wfobj.GetNamespace(),
			wfobj.GetName())
		c.logger.Info(msg)
		return fmt.Errorf(msg)
	}

	err = c.updateAddonStatus(wfobj.GetNamespace(), addonName, lifecycle, wfobj.Status.Phase)
	if err != nil {
		msg := fmt.Sprintf("\n### error failed updating %s status err %#v\n", addonName, err)
		c.logger.Info(msg)
		return fmt.Errorf(msg)
	}

	return nil
}

func (c *ResourceController) updateAddonStatus(namespace, name, lifecycle string, lifecyclestatus wfv1.WorkflowPhase) error {
	msg := fmt.Sprintf("updating addon %s/%s step %s status to %s\n", namespace, name, lifecycle, lifecyclestatus)
	c.logger.Info(msg)

	//addonobj, err := c.addonlister.Addons(namespace).Get(name)
	key := fmt.Sprintf("%s/%s", namespace, name)
	obj, exists, err := c.informer.GetIndexer().GetByKey(key)
	if err != nil || !exists {
		msg := fmt.Sprintf("\n### error failed getting addon %s/%s, err %v", namespace, name, err)
		c.logger.Info(msg)
		return fmt.Errorf(msg)
	}
	addonobj, err := FromUnstructured(obj.(*unstructured.Unstructured))
	if err != nil {
		msg := fmt.Sprintf("\n### error converting un to addon,  err %v", err)
		c.logger.Info(msg)
		return fmt.Errorf(msg)
	}

	updating := addonobj.DeepCopy()
	prevStatus := addonobj.Status
	cycle := prevStatus.Lifecycle
	msg = fmt.Sprintf("addon %s/%s cycle %v", updating.Namespace, updating.Name, cycle)
	c.logger.Info(msg)

	newStatus := addonv1.AddonStatus{
		Lifecycle: addonv1.AddonStatusLifecycle{},
		Resources: []addonv1.ObjectStatus{},
	}
	if lifecycle == "prereqs" {
		newStatus.Lifecycle.Prereqs = addonv1.ApplicationAssemblyPhase(lifecyclestatus)
		newStatus.Lifecycle.Installed = prevStatus.Lifecycle.Installed
	} else if lifecycle == "install" || lifecycle == "delete" {
		newStatus.Lifecycle.Installed = addonv1.ApplicationAssemblyPhase(lifecyclestatus)
		newStatus.Lifecycle.Prereqs = prevStatus.Lifecycle.Prereqs
	}
	newStatus.Resources = append(newStatus.Resources, prevStatus.Resources...)
	newStatus.Checksum = prevStatus.Checksum
	newStatus.Reason = prevStatus.Reason
	newStatus.StartTime = prevStatus.StartTime

	if reflect.DeepEqual(newStatus, prevStatus) {
		msg := fmt.Sprintf("addon %s/%s lifecycle the same. skip update.", updating.Namespace, updating.Name)
		c.logger.Info(msg)
		return nil
	}
	updating.Status = newStatus
	updated, err := c.addoncli.AddonmgrV1alpha1().Addons(updating.Namespace).UpdateStatus(context.Background(), updating,
		metav1.UpdateOptions{})
	if err != nil {
		msg := fmt.Sprintf("Addon %s/%s status could not be updated. %v", updating.Namespace, updating.Name, err)
		fmt.Print(msg)
	}
	cycle = updated.Status.Lifecycle
	msg = fmt.Sprintf("addon %s/%s updated cycle %v", updating.Namespace, updating.Namespace, cycle)
	c.logger.Info(msg)

	if lifecycle == "delete" && updating.Status.Lifecycle.Installed.Completed() {
		//delAddon(updating.Namespace, updating.Namespace)
	}

	msg = fmt.Sprintf("successfully update addon %s/%s step %s status to %s", namespace, name, lifecycle, lifecyclestatus)
	c.logger.Info(msg)
	return nil
}

func (c *ResourceController) delAddon(namespace, name string) error {
	delseconds := int64(0)
	err := c.addoncli.AddonmgrV1alpha1().Addons(namespace).Delete(context.TODO(), name, metav1.DeleteOptions{GracePeriodSeconds: &delseconds})
	if err != nil {
		msg := fmt.Sprintf("failed deleting addon %s/%s, err %v", namespace, name, err)
		fmt.Print(msg)
		return fmt.Errorf(msg)
	}
	msg := fmt.Sprintf("\n Successfully deleting addon %s/%s\n", namespace, name)
	fmt.Print(msg)
	return nil
}

func (c *ResourceController) handleWorkFlowDeletion(ctx context.Context, obj interface{}) error {
	fmt.Printf("\n yes, detect wf deletion \n")

	// Remove version from cache
	// if ok, v := r.versionCache.HasVersionName(req.Name); ok {
	// 	r.versionCache.RemoveVersion(v.PkgName, v.PkgVersion)
	// }
	return nil
}

func (c *ResourceController) createAddon(ctx context.Context, instance *addonv1.Addon, wfl workflows.AddonLifecycle) error {

	errors := []error{}

	// Validate it is our addon with our app label

	// Set finalizer only after addon is valid
	if err := c.SetFinalizer(ctx, instance, addonapiv1.FinalizerName); err != nil {
		reason := fmt.Sprintf("Addon %s/%s could not add finalizer. %v", instance.Namespace, instance.Name, err)
		c.Recorder.Event(instance, "Warning", "Failed", reason)
		c.logger.Error(err, "Failed to add finalizer for addon.")
		instance.Status.Lifecycle.Installed = addonv1.Failed
		instance.Status.Reason = reason
		if err := c.updateAddon(instance); err != nil {
			fmt.Printf("### error failed setting finalizer, err ", err)
			panic(err)
		}
		return err
	}
	// Record successful validation
	c.Recorder.Event(instance, "Normal", "Completed", fmt.Sprintf("setup addon %s/%s finalizer.", instance.Namespace, instance.Name))

	_, instance.Status.Checksum = c.validateChecksum(instance)
	fmt.Printf("\n addon %s/%s spec configure checksum \n", instance.Namespace, instance.Name)
	instance.Status = addonv1.AddonStatus{
		StartTime: common.GetCurretTimestamp(),
		Lifecycle: addonv1.AddonStatusLifecycle{
			Prereqs:   "",
			Installed: "",
		},
		Reason:    "",
		Resources: make([]addonv1.ObjectStatus, 0),
	}

	err := c.executePrereqAndInstall(ctx, instance, wfl)
	if err != nil {
		fmt.Printf("\n failed installing addon %s/%s prereqs and instll err %v\n", instance.Namespace, instance.Name, err)
		errors = append(errors, err)
	}

	// if err := c.updateAddon(instance); err != nil {
	// 	fmt.Printf("\n failed updating addon %s/%s status err %v\n", err)
	// 	panic(err)
	// }
	// Record successful validation
	c.Recorder.Event(instance, "Normal", "Completed", fmt.Sprintf("Addon %s/%s workflow is executed.", instance.Namespace, instance.Name))
	fmt.Printf("\n either workflow update my status or my installation timeout after re-queue.\n")
	// Observe resources matching selector labels.
	// Use k8s resource update trigger update
	// observed, err := r.observeResources(ctx, instance)
	// if err != nil {
	// 	reason := fmt.Sprintf("Addon %s/%s failed to find deployed resources. %v", instance.Namespace, instance.Name, err)
	// 	c.Recorder.Event(instance, "Warning", "Failed", reason)
	// 	log.Error(err, "Addon failed to find deployed resources.")
	// 	instance.Status.Lifecycle.Installed = addonv1.Failed
	// 	instance.Status.Reason = reason

	// 	return reconcile.Result{}, err
	// }

	// if len(observed) > 0 {
	// 	instance.Status.Resources = observed
	// }

	return nil
}

func (c *ResourceController) executePrereqAndInstall(ctx context.Context, instance *addonv1.Addon, wfl workflows.AddonLifecycle) error {

	prereqsPhase, err := c.runWorkflow(addonv1.Prereqs, instance, wfl)
	if err != nil {
		reason := fmt.Sprintf("Addon %s/%s prereqs failed. %v", instance.Namespace, instance.Name, err)
		c.Recorder.Event(instance, "Warning", "Failed", reason)
		c.logger.Error(err, "Addon prereqs workflow failed.")
		// if prereqs failed, set install status to failed as well so that STATUS is updated
		instance.Status.Lifecycle.Installed = addonv1.Failed
		instance.Status.Reason = reason
		fmt.Printf("\n### error addon %s/%s prereq failed %v \n", instance.Namespace, instance.Name, err)
		return err
	}
	instance.Status.Lifecycle.Prereqs = prereqsPhase

	if err := c.validateSecrets(ctx, instance); err != nil {
		reason := fmt.Sprintf("Addon %s/%s could not validate secrets. %v", instance.Namespace, instance.Name, err)
		c.Recorder.Event(instance, "Warning", "Failed", reason)
		c.logger.Error(err, "Addon could not validate secrets.")
		instance.Status.Lifecycle.Installed = addonv1.Failed
		instance.Status.Reason = reason
		fmt.Printf("\n### error addon %s/%s secret validation failed %v \n", instance.Namespace, instance.Name, err)
		return err
	}

	phase, err := c.runWorkflow(addonv1.Install, instance, wfl)
	instance.Status.Lifecycle.Installed = phase
	if err != nil {
		reason := fmt.Sprintf("Addon %s/%s could not be installed due to error. %v", instance.Namespace, instance.Name, err)
		c.Recorder.Event(instance, "Warning", "Failed", reason)
		c.logger.Error(err, "Addon install workflow failed.")
		instance.Status.Reason = reason
		fmt.Printf("\n### error addon %s/%s install failed %v \n", instance.Namespace, instance.Name, err)
		return err
	}

	return nil
}

func (c *ResourceController) validateSecrets(ctx context.Context, addon *addonv1.Addon) error {
	foundSecrets, err := c.dynCli.Resource(common.SecretGVR()).Namespace(addon.Spec.Params.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	secretsList := make(map[string]struct{}, len(foundSecrets.Items))
	for _, foundSecret := range foundSecrets.Items {
		secretsList[foundSecret.UnstructuredContent()["metadata"].(map[string]interface{})["name"].(string)] = struct{}{}
	}

	for _, secret := range addon.Spec.Secrets {
		if _, ok := secretsList[secret.Name]; !ok {
			return fmt.Errorf("addon %s needs secret \"%s\" that was not found in namespace %s", addon.Name, secret.Name, addon.Spec.Params.Namespace)
		}
	}

	return nil
}

func (c *ResourceController) addAddonToCache(instance *addonv1.Addon) {
	var version = addon.Version{
		Name:        instance.GetName(),
		Namespace:   instance.GetNamespace(),
		PackageSpec: instance.GetPackageSpec(),
		PkgPhase:    instance.GetInstallStatus(),
	}
	c.versionCache.AddVersion(version)
	c.logger.Info("Adding version cache", "phase ", version.PkgPhase)
}

func (c *ResourceController) runWorkflow(lifecycleStep addonv1.LifecycleStep, addon *addonv1.Addon, wfl workflows.AddonLifecycle) (addonv1.ApplicationAssemblyPhase, error) {

	wt, err := addon.GetWorkflowType(lifecycleStep)
	if err != nil {
		c.logger.Error(err, "lifecycleStep is not a field in LifecycleWorkflowSpec", "lifecycleStep", lifecycleStep)
		fmt.Printf("\n\n ### error failed workflow type %#v", err)
		return addonv1.Failed, err
	}

	if wt.Template == "" {
		// No workflow was provided, so mark as succeeded
		fmt.Printf("\n\n empty workflow")
		return addonv1.Succeeded, nil
	}

	wfIdentifierName := addon.GetFormattedWorkflowName(lifecycleStep)
	if wfIdentifierName == "" {
		fmt.Printf("\n\n ### error wfIdentifierName empty")
		return addonv1.Failed, fmt.Errorf("could not generate workflow template name")
	}
	phase, err := wfl.Install(context.TODO(), wt, wfIdentifierName)
	if err != nil {
		fmt.Printf("\n\n ### error workflow install %s failure %#v ", wfIdentifierName, err)
		return phase, err
	}
	c.Recorder.Event(addon, "Normal", "Completed", fmt.Sprintf("Completed %s workflow %s/%s.", strings.Title(string(lifecycleStep)), addon.Namespace, wfIdentifierName))
	return phase, nil
}

// Calculates new checksum and validates if there is a diff
func (c *ResourceController) validateChecksum(instance *addonv1.Addon) (bool, string) {
	newCheckSum := instance.CalculateChecksum()

	if instance.Status.Checksum == newCheckSum {
		return false, newCheckSum
	}

	return true, newCheckSum
}

// SetFinalizer adds finalizer to addon instances
func (c *ResourceController) SetFinalizer(ctx context.Context, addon *addonv1.Addon, finalizerName string) error {
	// Resource is not being deleted
	if addon.ObjectMeta.DeletionTimestamp.IsZero() {
		// And does not contain finalizer
		if !common.ContainsString(addon.ObjectMeta.Finalizers, finalizerName) {
			// Set Finalizer
			addon.ObjectMeta.Finalizers = append(addon.ObjectMeta.Finalizers, finalizerName)
		}
	}

	return nil
}

// func (c *ResourceController) updateAddonStatus(old, new *addonv1.Addon) error {
// 	if reflect.DeepEqual(old, new) {
// 		return nil
// 	}

// 	_, err := c.addoncli.AddonmgrV1alpha1().Addons(new.Namespace).Update(context.Background(), new,
// 		metav1.UpdateOptions{})
// 	if err != nil {
// 		msg := fmt.Sprintf("Failed updating addon %s/%s . %v", new.Namespace, new.Name, err)
// 		fmt.Print(msg)
// 		return fmt.Errorf(msg)
// 	}
// 	return nil
// }

func (c *ResourceController) updateAddon(addon *addonv1.Addon) error {
	_, err := c.addoncli.AddonmgrV1alpha1().Addons(addon.Namespace).Update(context.Background(), addon,
		metav1.UpdateOptions{})
	if err != nil {
		msg := fmt.Sprintf("Failed updating addon %s/%s . %v", addon.Namespace, addon.Name, err)
		fmt.Printf("\n###error failed updating addon %s/%s status err %#v\n", addon.Namespace, addon.Name, err)
		fmt.Print(msg)
		return fmt.Errorf(msg)
	}
	return nil
}
