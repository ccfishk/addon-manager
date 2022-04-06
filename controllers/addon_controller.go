/*
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package controllers

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	// "github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	batchv1beta1 "k8s.io/api/batch/v1beta1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	addonmgrv1alpha1 "github.com/keikoproj/addon-manager/api/addon/v1alpha1"
	"github.com/keikoproj/addon-manager/pkg/addon"
	"github.com/keikoproj/addon-manager/pkg/common"
	"github.com/keikoproj/addon-manager/pkg/workflows"
	"k8s.io/client-go/dynamic/dynamicinformer"

	wfclientset "github.com/argoproj/argo-workflows/v3/pkg/client/clientset/versioned"
	"k8s.io/client-go/tools/cache"
)

const (
	controllerName = "addon-manager-controller"
	// addon ttl time
	TTL = time.Duration(1) * time.Hour // 1 hour

	workflowDeployedNS = "addon-manager-system"

	workflowResyncPeriod = 20 * time.Minute
)

// Watched resources
var (
	resources = [...]runtime.Object{
		&v1.Service{TypeMeta: metav1.TypeMeta{Kind: "Service", APIVersion: "v1"}},
		&batchv1.Job{TypeMeta: metav1.TypeMeta{Kind: "Job", APIVersion: "batch/v1"}}, &batchv1beta1.CronJob{TypeMeta: metav1.TypeMeta{Kind: "CronJob", APIVersion: "batch/v1beta1"}},
		&appsv1.Deployment{TypeMeta: metav1.TypeMeta{Kind: "Deployment", APIVersion: "apps/v1"}},
		&appsv1.DaemonSet{TypeMeta: metav1.TypeMeta{Kind: "DaemonSet", APIVersion: "apps/v1"}},
		&appsv1.ReplicaSet{TypeMeta: metav1.TypeMeta{Kind: "ReplicaSet", APIVersion: "apps/v1"}},
		&appsv1.StatefulSet{TypeMeta: metav1.TypeMeta{Kind: "StatefulSet", APIVersion: "apps/v1"}},
	}
	finalizerName      = "delete.addonmgr.keikoproj.io"
	generatedInformers informers.SharedInformerFactory
)

// AddonReconciler reconciles a Addon object
type AddonReconciler struct {
	client.Client
	Log          logr.Logger
	Scheme       *runtime.Scheme
	versionCache addon.VersionCacheClient
	dynClient    dynamic.Interface
	recorder     record.EventRecorder
	statusWGMap  map[string]*sync.WaitGroup

	wfcli      wfclientset.Interface
	wfinformer cache.SharedIndexInformer
}

// NewAddonReconciler returns an instance of AddonReconciler
func NewAddonReconciler(mgr manager.Manager) *AddonReconciler {

	wfcli := common.NewWFClient(mgr.GetConfig())
	if wfcli == nil {
		panic("workflow client could not be nil")
	}

	return &AddonReconciler{
		Client:       mgr.GetClient(),
		Log:          ctrl.Log.WithName(controllerName),
		Scheme:       mgr.GetScheme(),
		versionCache: addon.NewAddonVersionCacheClient(),
		dynClient:    dynamic.NewForConfigOrDie(mgr.GetConfig()),
		recorder:     mgr.GetEventRecorderFor("addons"),
		statusWGMap:  map[string]*sync.WaitGroup{},
		wfcli:        wfcli,
	}
}

// +kubebuilder:rbac:groups=addonmgr.keikoproj.io,resources=addons,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=addonmgr.keikoproj.io,resources=addons/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=argoproj.io,resources=workflows,namespace=system,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,namespace=system,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=list
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings,verbs=get;list;patch;create
// +kubebuilder:rbac:groups="",resources=namespaces;clusterroles;configmaps;events;pods;serviceaccounts;services,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments;daemonsets;replicasets;statefulsets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=extensions,resources=deployments;daemonsets;replicasets;ingresses,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs;cronjobs,verbs=get;list;watch;create;update;patch

// Reconcile method for all addon requests
func (r *AddonReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("addon", req.NamespacedName)

	log.Info("Starting addon-manager reconcile...")
	var instance = &addonmgrv1alpha1.Addon{}
	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		log.Info("Addon not found.")

		// Remove version from cache
		if ok, v := r.versionCache.HasVersionName(req.Name); ok {
			r.versionCache.RemoveVersion(v.PkgName, v.PkgVersion)
		}

		return reconcile.Result{}, ignoreNotFound(err)
	}

	return r.execAddon(ctx, req, log, instance)
}

func (r *AddonReconciler) execAddon(ctx context.Context, req reconcile.Request, log logr.Logger, instance *addonmgrv1alpha1.Addon) (reconcile.Result, error) {
	defer func() {
		if err := recover(); err != nil {
			log.Info("Error: Panic occurred during execAdd %s/%s due to %s", instance.Namespace, instance.Name, err)
		}
	}()

	var wfl = workflows.NewWorkflowLifecycle(r.wfcli, r.wfinformer, r.dynClient, instance, r.Scheme, r.recorder, r.Log)

	// Resource is being deleted, run finalizers and exit.
	if !instance.ObjectMeta.DeletionTimestamp.IsZero() {
		// For a better user experience we want to update the status and requeue
		if instance.Status.Lifecycle.Installed != addonmgrv1alpha1.Deleting {
			instance.Status.Lifecycle.Installed = addonmgrv1alpha1.Deleting
			log.Info("Requeue to set deleting status")
			err := r.updateAddonStatus(ctx, log, instance)
			return reconcile.Result{}, err
		}

		err := r.Finalize(ctx, instance, wfl, finalizerName)
		if err != nil {
			reason := fmt.Sprintf("Addon %s/%s could not be finalized. %v", instance.Namespace, instance.Name, err)
			r.recorder.Event(instance, "Warning", "Failed", reason)
			instance.Status.Lifecycle.Installed = addonmgrv1alpha1.DeleteFailed
			instance.Status.Reason = reason
			log.Error(err, "Failed to finalize addon.")
			return reconcile.Result{}, err
		}
		// Requeue to remove from caches
		return reconcile.Result{Requeue: true}, nil
	}

	// Process addon instance
	ret, procErr := r.processAddon(ctx, log, instance, wfl)

	// Always update cache, status except errors
	r.addAddonToCache(log, instance)

	err := r.updateAddonStatus(ctx, log, instance)
	if err != nil {
		// Force retry when status fails to update
		return reconcile.Result{RequeueAfter: 1 * time.Second}, err
	}

	return ret, procErr
}

func New(mgr manager.Manager, stopChan <-chan struct{}) (controller.Controller, error) {
	r := NewAddonReconciler(mgr)
	r.wfinformer = common.NewWorkflowInformer(r.dynClient, workflowDeployedNS, workflowResyncPeriod, cache.Indexers{}, func(options *metav1.ListOptions) {})
	go r.wfinformer.Run(stopChan)

	c, err := controller.New(controllerName, mgr, controller.Options{Reconciler: r})
	if err != nil {
		return nil, err
	}

	// Watch workflows created by addon only in addon-manager-system namespace
	ctx := context.Background()
	nsInformers := dynamicinformer.NewFilteredDynamicSharedInformerFactory(r.dynClient, time.Minute*30, workflowDeployedNS, nil)
	wfInf := nsInformers.ForResource(common.WorkflowGVR())
	wfInf.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			_, err := cache.MetaNamespaceKeyFunc(obj)
			if err == nil {
				//newEvent.key = key
				//newEvent.eventType = "create"

				//logrus.WithField("controllers", "workflow").Infof("Processing add to %v: %s", resourceType, newEvent.key)
				c.handleWorkFlowAdd(ctx, obj)
			}
		},
		UpdateFunc: func(old, new interface{}) {
			_, err := cache.MetaNamespaceKeyFunc(new)
			if err == nil {
				//newEvent.key = key
				//newEvent.eventType = "update"

				//logrus.WithField("controllers", "workflow").Infof("Processing update to %v: %s", resourceType, newEvent.key)
				c.handleWorkFlowUpdate(ctx, new)
			}
		},
	})
	if err := c.Watch(&source.Informer{Informer: wfInf.Informer()}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &addonmgrv1alpha1.Addon{},
	}, predicate.NewPredicateFuncs(r.workflowHasMatchingNamespace)); err != nil {
		return nil, err
	}

	wfInforms := NewWfInformers(nsInformers, stopChan)
	err = mgr.Add(wfInforms)
	if err != nil {
		return nil, fmt.Errorf("failed to start workflowinformers")
	}

	// Watch for changes to kubernetes Resources matching addon labels.
	if err := c.Watch(&source.Kind{Type: &addonmgrv1alpha1.Addon{}}, &handler.EnqueueRequestForObject{}); err != nil {
		return nil, err
	}

	if err := c.Watch(&source.Kind{Type: &appsv1.Deployment{}}, r.enqueueRequestWithAddonLabel()); err != nil {
		return nil, err
	}

	if err := c.Watch(&source.Kind{Type: &v1.Service{}}, r.enqueueRequestWithAddonLabel()); err != nil {
		return nil, err
	}

	if err := c.Watch(&source.Kind{Type: &appsv1.DaemonSet{}}, r.enqueueRequestWithAddonLabel()); err != nil {
		return nil, err
	}

	if err := c.Watch(&source.Kind{Type: &appsv1.ReplicaSet{}}, r.enqueueRequestWithAddonLabel()); err != nil {
		return nil, err
	}

	if err := c.Watch(&source.Kind{Type: &appsv1.StatefulSet{}}, r.enqueueRequestWithAddonLabel()); err != nil {
		return nil, err
	}

	if err := c.Watch(&source.Kind{Type: &batchv1.Job{}}, r.enqueueRequestWithAddonLabel()); err != nil {
		return nil, err
	}
	return c, nil
}

func (r *AddonReconciler) workflowHasMatchingNamespace(obj client.Object) bool {
	u, _ := obj.(*unstructured.Unstructured)
	if u.GetObjectKind().GroupVersionKind() != common.WorkflowType().GroupVersionKind() {
		r.Log.Error(fmt.Errorf("unexpected object type in workflow watch predicates"), "expected", "*wfv1.Workflow", "found", reflect.TypeOf(obj))
		return false
	}
	if obj.GetNamespace() != workflowDeployedNS {
		return false
	}
	return true
}

func (r *AddonReconciler) enqueueRequestWithAddonLabel() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(a client.Object) []reconcile.Request {
		var reqs = make([]reconcile.Request, 0)
		var labels = a.GetLabels()
		if name, ok := labels["app.kubernetes.io/name"]; ok && strings.TrimSpace(name) != "" {
			// Let's lookup addon related to this object.
			if ok, v := r.versionCache.HasVersionName(name); ok {
				reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{
					Name:      v.Name,
					Namespace: v.Namespace,
				}})
			}
		}
		return reqs
	})
}

func (r *AddonReconciler) processAddon(ctx context.Context, log logr.Logger, instance *addonmgrv1alpha1.Addon, wfl workflows.AddonLifecycle) (reconcile.Result, error) {

	// Calculate Checksum, returns true if checksum is not changed
	var changedStatus bool
	changedStatus, instance.Status.Checksum = r.validateChecksum(instance)

	// Resources list
	instance.Status.Resources = make([]addonmgrv1alpha1.ObjectStatus, 0)

	if changedStatus {
		// Set ttl starttime if checksum has changed
		instance.Status.StartTime = common.GetCurretTimestamp()

		// Clear out status and reason
		instance.Status.Lifecycle.Prereqs = ""
		instance.Status.Lifecycle.Installed = ""
		instance.Status.Reason = ""
	}

	// Update status that we have started reconciling this addon.
	if instance.Status.Lifecycle.Installed == "" {
		instance.Status.Lifecycle.Installed = addonmgrv1alpha1.Pending
		log.Info("Requeue to set pending status")
		return reconcile.Result{Requeue: true}, nil
	}

	// Check if addon installation expired.
	if instance.Status.Lifecycle.Installed == addonmgrv1alpha1.Pending && common.IsExpired(instance.Status.StartTime, TTL.Milliseconds()) {
		reason := fmt.Sprintf("Addon %s/%s ttl expired, starttime exceeded %s", instance.Namespace, instance.Name, TTL.String())
		r.recorder.Event(instance, "Warning", "Failed", reason)
		err := fmt.Errorf(reason)
		log.Error(err, reason)

		instance.Status.Lifecycle.Installed = addonmgrv1alpha1.Failed
		instance.Status.Reason = reason

		return reconcile.Result{}, err
	}

	// Validate Addon
	if ok, err := addon.NewAddonValidator(instance, r.versionCache, r.dynClient).Validate(); !ok {
		// if an addons dependency is in a Pending state then make the parent addon Pending
		if err != nil && strings.HasPrefix(err.Error(), addon.ErrDepPending) {
			reason := fmt.Sprintf("Addon %s/%s is waiting on dependencies to be out of Pending state.", instance.Namespace, instance.Name)
			// Record an event if addon is not valid
			r.recorder.Event(instance, "Normal", "Pending", reason)
			instance.Status.Lifecycle.Installed = addonmgrv1alpha1.Pending
			instance.Status.Reason = reason

			log.Info(reason)

			// requeue after 10 seconds
			return reconcile.Result{
				Requeue:      true,
				RequeueAfter: 10 * time.Second,
			}, nil
		} else if err != nil && strings.HasPrefix(err.Error(), addon.ErrDepNotInstalled) {
			reason := fmt.Sprintf("Addon %s/%s is waiting on dependencies to be installed. %v", instance.Namespace, instance.Name, err)
			// Record an event if addon is not valid
			r.recorder.Event(instance, "Normal", "Failed", reason)
			instance.Status.Lifecycle.Installed = addonmgrv1alpha1.ValidationFailed
			instance.Status.Reason = reason

			log.Info(reason)

			// requeue after 30 seconds
			return reconcile.Result{
				Requeue:      true,
				RequeueAfter: 30 * time.Second,
			}, nil
		}

		reason := fmt.Sprintf("Addon %s/%s is not valid. %v", instance.Namespace, instance.Name, err)
		// Record an event if addon is not valid
		r.recorder.Event(instance, "Warning", "Failed", reason)
		instance.Status.Lifecycle.Installed = addonmgrv1alpha1.ValidationFailed
		instance.Status.Reason = reason

		log.Error(err, "Failed to validate addon.")

		return reconcile.Result{}, err
	}

	// Record successful validation
	r.recorder.Event(instance, "Normal", "Completed", fmt.Sprintf("Addon %s/%s is valid.", instance.Namespace, instance.Name))

	// Set finalizer only after addon is valid
	if err := r.SetFinalizer(ctx, instance, finalizerName); err != nil {
		reason := fmt.Sprintf("Addon %s/%s could not add finalizer. %v", instance.Namespace, instance.Name, err)
		r.recorder.Event(instance, "Warning", "Failed", reason)
		log.Error(err, "Failed to add finalizer for addon.")
		instance.Status.Lifecycle.Installed = addonmgrv1alpha1.Failed
		instance.Status.Reason = reason
		return reconcile.Result{}, err
	}

	// Execute PreReq and Install workflow, if spec body has changed.
	// In the case when validation failed and continued here we should execute.
	// Also if workflow is in Pending state, execute it to update status to terminal state.
	if changedStatus || instance.Status.Lifecycle.Installed == addonmgrv1alpha1.ValidationFailed ||
		instance.Status.Lifecycle.Prereqs == addonmgrv1alpha1.Pending || instance.Status.Lifecycle.Installed == addonmgrv1alpha1.Pending {
		log.Info("Addon spec is updated, workflows will be generated")

		err := r.executePrereqAndInstall(ctx, log, instance, wfl)
		if err != nil {
			return reconcile.Result{}, err
		}
	}

	// Observe resources matching selector labels.
	observed, err := r.observeResources(ctx, instance)
	if err != nil {
		reason := fmt.Sprintf("Addon %s/%s failed to find deployed resources. %v", instance.Namespace, instance.Name, err)
		r.recorder.Event(instance, "Warning", "Failed", reason)
		log.Error(err, "Addon failed to find deployed resources.")
		instance.Status.Lifecycle.Installed = addonmgrv1alpha1.Failed
		instance.Status.Reason = reason

		return reconcile.Result{}, err
	}

	if len(observed) > 0 {
		instance.Status.Resources = observed
	}

	return ctrl.Result{}, nil
}

func ignoreNotFound(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (r *AddonReconciler) runWorkflow(lifecycleStep addonmgrv1alpha1.LifecycleStep, addon *addonmgrv1alpha1.Addon, wfl workflows.AddonLifecycle) (addonmgrv1alpha1.ApplicationAssemblyPhase, error) {
	log := r.Log.WithValues("addon", fmt.Sprintf("%s/%s", addon.Namespace, addon.Name))

	wt, err := addon.GetWorkflowType(lifecycleStep)
	if err != nil {
		log.Error(err, "lifecycleStep is not a field in LifecycleWorkflowSpec", "lifecycleStep", lifecycleStep)
		return addonmgrv1alpha1.Failed, err
	}

	if wt.Template == "" {
		// No workflow was provided, so mark as succeeded
		return addonmgrv1alpha1.Succeeded, nil
	}

	wfIdentifierName := addon.GetFormattedWorkflowName(lifecycleStep)
	if wfIdentifierName == "" {
		return addonmgrv1alpha1.Failed, fmt.Errorf("could not generate workflow template name")
	}
	phase, err := wfl.Install(context.TODO(), wt, wfIdentifierName)
	if err != nil {
		return phase, err
	}
	r.recorder.Event(addon, "Normal", "Completed", fmt.Sprintf("Completed %s workflow %s/%s.", strings.Title(string(lifecycleStep)), addon.Namespace, wfIdentifierName))
	return phase, nil
}

func (r *AddonReconciler) validateSecrets(ctx context.Context, addon *addonmgrv1alpha1.Addon) error {
	foundSecrets, err := r.dynClient.Resource(common.SecretGVR()).Namespace(addon.Spec.Params.Namespace).List(ctx, metav1.ListOptions{})
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

func (r *AddonReconciler) updateAddonStatus(ctx context.Context, log logr.Logger, addon *addonmgrv1alpha1.Addon) error {
	addonName := types.NamespacedName{Name: addon.Name, Namespace: addon.Namespace}.String()
	wg, ok := r.statusWGMap[addonName]
	if !ok {
		wg = &sync.WaitGroup{}
		r.statusWGMap[addonName] = wg
	}
	// Wait to process addon updates until we have finished updating same addon
	wg.Wait()
	wg.Add(1)
	defer wg.Done()
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		return r.Status().Update(ctx, addon, &client.UpdateOptions{})
	})
	if err != nil {
		log.Error(err, "Addon status could not be updated.")
		r.recorder.Event(addon, "Warning", "Failed", fmt.Sprintf("Addon %s/%s status could not be updated. %v", addon.Namespace, addon.Name, err))
		return err
	}

	return nil
}

func (r *AddonReconciler) addAddonToCache(log logr.Logger, instance *addonmgrv1alpha1.Addon) {
	var version = addon.Version{
		Name:        instance.GetName(),
		Namespace:   instance.GetNamespace(),
		PackageSpec: instance.GetPackageSpec(),
		PkgPhase:    instance.GetInstallStatus(),
	}
	r.versionCache.AddVersion(version)
	log.Info("Adding version cache", "phase", version.PkgPhase)
}

func (r *AddonReconciler) executePrereqAndInstall(ctx context.Context, log logr.Logger, instance *addonmgrv1alpha1.Addon, wfl workflows.AddonLifecycle) error {
	// Always reset reason when executing
	instance.Status.Reason = ""
	prereqsPhase, err := r.runWorkflow(addonmgrv1alpha1.Prereqs, instance, wfl)
	if err != nil {
		reason := fmt.Sprintf("Addon %s/%s prereqs failed. %v", instance.Namespace, instance.Name, err)
		r.recorder.Event(instance, "Warning", "Failed", reason)
		log.Error(err, "Addon prereqs workflow failed.")
		// if prereqs failed, set install status to failed as well so that STATUS is updated
		instance.Status.Lifecycle.Installed = addonmgrv1alpha1.Failed
		instance.Status.Reason = reason

		return err
	}
	instance.Status.Lifecycle.Prereqs = prereqsPhase

	//handle Prereqs failure
	if instance.Status.Lifecycle.Prereqs == addonmgrv1alpha1.Failed {
		reason := fmt.Sprintf("Addon %s/%s Prereqs status is Failed", instance.Namespace, instance.Name)
		log.Error(err, "Addon prereqs workflow failed.")
		r.recorder.Event(instance, "Warning", "Failed", reason)
		// if prereqs failed, set install status to failed as well so that STATUS is updated
		instance.Status.Lifecycle.Installed = addonmgrv1alpha1.Failed
		instance.Status.Reason = reason

		return fmt.Errorf(reason)
	}

	if instance.Status.Lifecycle.Prereqs == addonmgrv1alpha1.Succeeded {
		if err := r.validateSecrets(ctx, instance); err != nil {
			reason := fmt.Sprintf("Addon %s/%s could not validate secrets. %v", instance.Namespace, instance.Name, err)
			r.recorder.Event(instance, "Warning", "Failed", reason)
			log.Error(err, "Addon could not validate secrets.")
			instance.Status.Lifecycle.Installed = addonmgrv1alpha1.Failed
			instance.Status.Reason = reason

			return err
		}

		phase, err := r.runWorkflow(addonmgrv1alpha1.Install, instance, wfl)
		instance.Status.Lifecycle.Installed = phase
		if err != nil {
			reason := fmt.Sprintf("Addon %s/%s could not be installed due to error. %v", instance.Namespace, instance.Name, err)
			r.recorder.Event(instance, "Warning", "Failed", reason)
			log.Error(err, "Addon install workflow failed.")
			instance.Status.Reason = reason

			return err
		}
	}

	return nil
}

func (r *AddonReconciler) observeResources(ctx context.Context, a *addonmgrv1alpha1.Addon) ([]addonmgrv1alpha1.ObjectStatus, error) {
	var observed []addonmgrv1alpha1.ObjectStatus
	var labelSelector = a.Spec.Selector

	if len(labelSelector.MatchLabels) == 0 {
		labelSelector.MatchLabels = make(map[string]string)
	}
	// Always add app.kubernetes.io/managed-by and app.kubernetes.io/name to label selector
	labelSelector.MatchLabels["app.kubernetes.io/managed-by"] = common.AddonGVR().Group
	labelSelector.MatchLabels["app.kubernetes.io/name"] = fmt.Sprintf("%s", a.GetName())

	selector, err := metav1.LabelSelectorAsSelector(&labelSelector)
	if err != nil {
		return observed, fmt.Errorf("label selector is invalid. %v", err)
	}

	var errs []error
	cli := r.Client

	res, err := ObserveService(cli, a.GetNamespace(), selector)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to observe resource %s for addon %s/%s: %w", "service", a.GetNamespace(), a.GetName(), err))
	}
	observed = append(observed, res...)
	res, err = ObserveJob(cli, a.GetNamespace(), selector)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to observe resource %s for addon %s/%s: %w", "job", a.GetNamespace(), a.GetName(), err))
	}
	observed = append(observed, res...)
	res, err = ObserveCronJob(cli, a.GetNamespace(), selector)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to observe resource %s for addon %s/%s: %w", "cronjob", a.GetNamespace(), a.GetName(), err))
	}
	observed = append(observed, res...)

	res, err = ObserveStatefulSet(cli, a.GetNamespace(), selector)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to observe resource %s for addon %s/%s: %w", "StatefulSet", a.GetNamespace(), a.GetName(), err))
	}
	observed = append(observed, res...)

	res, err = ObserveDeployment(cli, a.GetNamespace(), selector)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to observe resource %s for addon %s/%s: %w", "Deployment", a.GetNamespace(), a.GetName(), err))
	}
	observed = append(observed, res...)

	res, err = ObserveDaemonSet(cli, a.GetNamespace(), selector)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to observe resource %s for addon %s/%s: %w", "DaemonSet", a.GetNamespace(), a.GetName(), err))
	}
	observed = append(observed, res...)

	res, err = ObserveReplicaSet(cli, a.GetNamespace(), selector)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed to observe resource %s for addon %s/%s: %w", "ReplicaSet", a.GetNamespace(), a.GetName(), err))
	}
	observed = append(observed, res...)

	if len(errs) > 0 {
		return observed, fmt.Errorf("observed err %v", errs)
	}
	return observed, nil
}

// Calculates new checksum and validates if there is a diff
func (r *AddonReconciler) validateChecksum(instance *addonmgrv1alpha1.Addon) (bool, string) {
	newCheckSum := instance.CalculateChecksum()

	if instance.Status.Checksum == newCheckSum {
		return false, newCheckSum
	}

	return true, newCheckSum
}

// Finalize runs finalizer for addon
func (r *AddonReconciler) Finalize(ctx context.Context, addon *addonmgrv1alpha1.Addon, wfl workflows.AddonLifecycle, finalizerName string) error {
	// Has Delete workflow defined, let's run it.
	var removeFinalizer = true

	if addon.Spec.Lifecycle.Delete.Template != "" {

		removeFinalizer = false

		// Run delete workflow
		phase, err := r.runWorkflow(addonmgrv1alpha1.Delete, addon, wfl)
		if err != nil {
			return err
		}

		if phase == addonmgrv1alpha1.Succeeded || phase == addonmgrv1alpha1.Failed {
			// Wait for workflow to succeed or fail.
			removeFinalizer = true
		}
	}

	// Remove version from cache
	r.versionCache.RemoveVersion(addon.Spec.PkgName, addon.Spec.PkgVersion)

	// Remove finalizer from the list and update it.
	if removeFinalizer && common.ContainsString(addon.ObjectMeta.Finalizers, finalizerName) {
		addon.ObjectMeta.Finalizers = common.RemoveString(addon.ObjectMeta.Finalizers, finalizerName)
		if err := r.Update(ctx, addon); err != nil {
			return err
		}
	}

	return nil
}

// SetFinalizer adds finalizer to addon instances
func (r *AddonReconciler) SetFinalizer(ctx context.Context, addon *addonmgrv1alpha1.Addon, finalizerName string) error {
	// Resource is not being deleted
	if addon.ObjectMeta.DeletionTimestamp.IsZero() {
		// And does not contain finalizer
		if !common.ContainsString(addon.ObjectMeta.Finalizers, finalizerName) {
			// Set Finalizer
			addon.ObjectMeta.Finalizers = append(addon.ObjectMeta.Finalizers, finalizerName)
			if err := r.Update(ctx, addon); err != nil {
				return err
			}
		}
	}

	return nil
}
