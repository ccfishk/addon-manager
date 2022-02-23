package controllers

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	wfv1 "github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
	addonv1 "github.com/keikoproj/addon-manager/pkg/apis/addon/v1alpha1"
	"github.com/keikoproj/addon-manager/pkg/common"
	"github.com/keikoproj/addon-manager/pkg/workflows"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	addoninternal "github.com/keikoproj/addon-manager/pkg/addon"
)

const (
	finalizerName = "delete.addonmgr.keikoproj.io"
)

func (c *Controller) handleAddonCreation(ctx context.Context, addon *addonv1.Addon) error {
	c.logger.Info("creating addon ", addon.Namespace, "/", addon.Name)

	var wfl = workflows.NewWorkflowLifecycle(c.wfcli, c.informer, c.dynCli, addon, c.scheme, c.recorder)
	//fmt.Printf("testing workflow %v", wfl)
	procErr := c.createAddon(ctx, addon, wfl)
	if procErr != nil {
		return procErr
	}

	c.addAddonToCache(addon)

	return nil
}

func (c *Controller) handleAddonUpdate(ctx context.Context, addon *addonv1.Addon) error {
	c.logger.Info("updating addon %s/%s", addon.Namespace, addon.Name)
	return nil
}

func (c *Controller) handleAddonDeletion(ctx context.Context, addon *addonv1.Addon) error {
	c.logger.Info("deleting addon ", addon.Namespace, "/", addon.Name)
	return nil
}

func (c *Controller) updateAddonStatus(namespace, name, lifecycle string, lifecyclestatus wfv1.WorkflowPhase) error {
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
	addonobj, err := common.FromUnstructured(obj.(*unstructured.Unstructured))
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

func (c *Controller) createAddon(ctx context.Context, instance *addonv1.Addon, wfl workflows.AddonLifecycle) error {

	errors := []error{}
	// Validate it is our addon with our app label

	// Set finalizer only after addon is valid
	if err := c.SetFinalizer(ctx, instance, finalizerName); err != nil {
		reason := fmt.Sprintf("Addon %s/%s could not add finalizer. %v", instance.Namespace, instance.Name, err)
		c.recorder.Event(instance, "Warning", "Failed", reason)
		c.logger.Error(err, "Failed to add finalizer for addon.")
		instance.Status.Lifecycle.Installed = addonv1.Failed
		instance.Status.Reason = reason
		if err := c.updateAddon(instance); err != nil {
			fmt.Printf("failed setting finalizer, err ", err)
			panic(err)
		}
		return err
	}
	// Record successful validation
	c.recorder.Event(instance, "Normal", "Completed", fmt.Sprintf("setup addon %s/%s finalizer.", instance.Namespace, instance.Name))

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

	c.recorder.Event(instance, "Normal", "Completed", fmt.Sprintf("Addon %s/%s workflow is executed.", instance.Namespace, instance.Name))
	fmt.Printf("\n either workflow update my status or my installation timeout after re-queue.\n")

	return nil
}

func (c *Controller) executePrereqAndInstall(ctx context.Context, instance *addonv1.Addon, wfl workflows.AddonLifecycle) error {

	prereqsPhase, err := c.runWorkflow(addonv1.Prereqs, instance, wfl)
	if err != nil {
		reason := fmt.Sprintf("Addon %s/%s prereqs failed. %v", instance.Namespace, instance.Name, err)
		c.recorder.Event(instance, "Warning", "Failed", reason)
		c.logger.Error(err, "Addon prereqs workflow failed.")
		// if prereqs failed, set install status to failed as well so that STATUS is updated
		instance.Status.Lifecycle.Installed = addonv1.Failed
		instance.Status.Reason = reason
		fmt.Printf("\n addon %s/%s prereq failed %v \n", instance.Namespace, instance.Name, err)
		return err
	}
	instance.Status.Lifecycle.Prereqs = prereqsPhase

	if err := c.validateSecrets(ctx, instance); err != nil {
		reason := fmt.Sprintf("Addon %s/%s could not validate secrets. %v", instance.Namespace, instance.Name, err)
		c.recorder.Event(instance, "Warning", "Failed", reason)
		c.logger.Error(err, "Addon could not validate secrets.")
		instance.Status.Lifecycle.Installed = addonv1.Failed
		instance.Status.Reason = reason
		fmt.Printf("\n addon %s/%s secret validation failed %v \n", instance.Namespace, instance.Name, err)
		return err
	}

	phase, err := c.runWorkflow(addonv1.Install, instance, wfl)
	instance.Status.Lifecycle.Installed = phase
	if err != nil {
		reason := fmt.Sprintf("Addon %s/%s could not be installed due to error. %v", instance.Namespace, instance.Name, err)
		c.recorder.Event(instance, "Warning", "Failed", reason)
		c.logger.Error(err, "Addon install workflow failed.")
		instance.Status.Reason = reason
		fmt.Printf("\n addon %s/%s install failed %v \n", instance.Namespace, instance.Name, err)
		return err
	}

	return nil
}

func (c *Controller) validateSecrets(ctx context.Context, addon *addonv1.Addon) error {
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

func (c *Controller) addAddonToCache(instance *addonv1.Addon) {
	var version = addoninternal.Version{
		Name:        instance.GetName(),
		Namespace:   instance.GetNamespace(),
		PackageSpec: instance.GetPackageSpec(),
		PkgPhase:    instance.GetInstallStatus(),
	}
	c.versionCache.AddVersion(version)
	c.logger.Info("Adding version cache", "phase ", version.PkgPhase)
}

func (c *Controller) runWorkflow(lifecycleStep addonv1.LifecycleStep, addon *addonv1.Addon, wfl workflows.AddonLifecycle) (addonv1.ApplicationAssemblyPhase, error) {

	wt, err := addon.GetWorkflowType(lifecycleStep)
	if err != nil {
		c.logger.Error(err, "lifecycleStep is not a field in LifecycleWorkflowSpec", "lifecycleStep", lifecycleStep)
		return addonv1.Failed, err
	}

	if wt.Template == "" {
		// No workflow was provided, so mark as succeeded
		return addonv1.Succeeded, nil
	}

	wfIdentifierName := addon.GetFormattedWorkflowName(lifecycleStep)
	if wfIdentifierName == "" {
		return addonv1.Failed, fmt.Errorf("could not generate workflow template name")
	}
	phase, err := wfl.Install(context.TODO(), wt, wfIdentifierName)
	if err != nil {
		return phase, err
	}
	c.recorder.Event(addon, "Normal", "Completed", fmt.Sprintf("Completed %s workflow %s/%s.", strings.Title(string(lifecycleStep)), addon.Namespace, wfIdentifierName))
	return phase, nil
}

// Calculates new checksum and validates if there is a diff
func (c *Controller) validateChecksum(instance *addonv1.Addon) (bool, string) {
	newCheckSum := instance.CalculateChecksum()

	if instance.Status.Checksum == newCheckSum {
		return false, newCheckSum
	}

	return true, newCheckSum
}

// SetFinalizer adds finalizer to addon instances
func (c *Controller) SetFinalizer(ctx context.Context, addon *addonv1.Addon, finalizerName string) error {
	// Resource is not being deleted
	if addon.ObjectMeta.DeletionTimestamp.IsZero() {
		// And does not contain finalizer
		if !common.ContainsString(addon.ObjectMeta.Finalizers, finalizerName) {
			// Set Finalizer
			addon.ObjectMeta.Finalizers = append(addon.ObjectMeta.Finalizers, finalizerName)
			if err := c.updateAddon(addon); err != nil {
				panic(err)
			}
		}
	}

	return nil
}

func (c *Controller) updateAddon(addon *addonv1.Addon) error {
	_, err := c.addoncli.AddonmgrV1alpha1().Addons(addon.Namespace).Update(context.Background(), addon,
		metav1.UpdateOptions{})
	if err != nil {
		msg := fmt.Sprintf("Failed updating addon %s/%s . %v", addon.Namespace, addon.Name, err)
		fmt.Print(msg)
		return fmt.Errorf(msg)
	}
	return nil
}
