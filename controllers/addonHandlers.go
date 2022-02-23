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
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	addoninternal "github.com/keikoproj/addon-manager/pkg/addon"
	addonapiv1 "github.com/keikoproj/addon-manager/pkg/apis/addon"
)

func (c *Controller) handleAddonCreation(ctx context.Context, addon *addonv1.Addon) error {
	c.logger.Info("creating addon ", addon.Namespace, "/", addon.Name)

	var wfl = workflows.NewWorkflowLifecycle(c.wfcli, c.informer, c.dynCli, addon, c.scheme, c.recorder)

	err := c.createAddon(ctx, addon, wfl)
	if err != nil {
		c.logger.Error("failed creating addon err %v", err)
		return err
	}

	c.addAddonToCache(addon)

	return nil
}

func (c *Controller) handleAddonUpdate(ctx context.Context, addon *addonv1.Addon) error {
	c.logger.Info("updating addon ", addon.Namespace, "/", addon.Name)

	if !addon.ObjectMeta.DeletionTimestamp.IsZero() {
		var wfl = workflows.NewWorkflowLifecycle(c.wfcli, c.informer, c.dynCli, addon, c.scheme, c.recorder)

		err := c.Finalize(ctx, addon, wfl, addonapiv1.FinalizerName)
		if err != nil {
			reason := fmt.Sprintf("Addon %s/%s could not be finalized. err %v", addon.Namespace, addon.Name, err)
			c.recorder.Event(addon, "Warning", "Failed", reason)
			addon.Status.Lifecycle.Installed = addonv1.DeleteFailed
			addon.Status.Reason = reason
			c.logger.Error(reason)
			return fmt.Errorf(reason)
		}

		// For a better user experience we want to update the status and requeue
		if addon.Status.Lifecycle.Installed != addonv1.Deleting {
			addon.Status.Lifecycle.Installed = addonv1.Deleting
			if err := c.updateAddonStatus(ctx, addon); err != nil {
				c.logger.Error("failed updating ", addon.Namespace, "/", addon.Name, " deleting status ", err)
				return err
			}
		}
	}

	// Check if addon installation expired.
	if addon.Status.Lifecycle.Installed == addonv1.Pending && common.IsExpired(addon.Status.StartTime, addonapiv1.TTL.Milliseconds()) {
		reason := fmt.Sprintf("Addon %s/%s ttl expired, starttime exceeded %s", addon.Namespace, addon.Name, addonapiv1.TTL.String())
		c.recorder.Event(addon, "Warning", "Failed", reason)
		err := fmt.Errorf(reason)
		c.logger.Error(err, reason)

		addon.Status.Lifecycle.Installed = addonv1.Failed
		addon.Status.Reason = reason

		if err := c.updateAddonStatus(ctx, addon); err != nil {
			c.logger.Error("failed updating ", addon.Namespace, "/", addon.Name, " ttl expire status ", err)
			return err
		}
	}

	return nil
}

func (c *Controller) handleAddonDeletion(ctx context.Context, addon *addonv1.Addon) error {
	c.logger.Info("deleting addon ", addon.Namespace, "/", addon.Name)
	if !addon.ObjectMeta.DeletionTimestamp.IsZero() {
		var wfl = workflows.NewWorkflowLifecycle(c.wfcli, c.informer, c.dynCli, addon, c.scheme, c.recorder)

		err := c.Finalize(ctx, addon, wfl, addonapiv1.FinalizerName)
		if err != nil {
			reason := fmt.Sprintf("Addon %s/%s could not be finalized. %v", addon.Namespace, addon.Name, err)
			c.recorder.Event(addon, "Warning", "Failed", reason)
			addon.Status.Lifecycle.Installed = addonv1.DeleteFailed
			addon.Status.Reason = reason
			c.logger.Error(err, "Failed to finalize addon.")
			return fmt.Errorf(reason)
		}

		// For a better user experience we want to update the status and requeue
		if addon.Status.Lifecycle.Installed != addonv1.Deleting {
			addon.Status.Lifecycle.Installed = addonv1.Deleting

			if err := c.updateAddonStatus(ctx, addon); err != nil {
				c.logger.Error("failed updating ", addon.Namespace, "/", addon.Name, " deleting status ", err)
				return err
			}
		}
	}
	return nil
}

func (c *Controller) getAddon(key string) (*addonv1.Addon, error) {
	obj, exists, err := c.informer.GetIndexer().GetByKey(key)
	if err != nil || !exists {
		msg := fmt.Sprintf("failed getting addon %s, err %v", key, err)
		c.logger.Error(msg)
		return nil, fmt.Errorf(msg)
	}
	latest, err := common.FromUnstructured(obj.(*unstructured.Unstructured))
	if err != nil {
		msg := fmt.Sprintf("failed converting %s un to addon,  err %v", key, err)
		c.logger.Error(msg)
		return nil, fmt.Errorf(msg)
	}
	return latest, nil
}

func (c *Controller) updateAddonStatusLifecycle(ctx context.Context, namespace, name string, lifecycle string, lifecyclestatus wfv1.WorkflowPhase) error {
	c.logger.Info("updating addon ", namespace, "/", name, " ", lifecycle, " status to ", lifecyclestatus)

	key := fmt.Sprintf("%s/%s", namespace, name)
	obj, exists, err := c.informer.GetIndexer().GetByKey(key)
	if err != nil || !exists {
		msg := fmt.Sprintf("failed getting addon %s/%s, err %v", namespace, name, err)
		c.logger.Error(msg)
		return fmt.Errorf(msg)
	}
	latest, err := common.FromUnstructured(obj.(*unstructured.Unstructured))
	if err != nil {
		msg := fmt.Sprintf("failed converting un to addon,  err %v", err)
		c.logger.Error(msg)
		return fmt.Errorf(msg)
	}
	updating := latest.DeepCopy()

	prevStatus := latest.Status
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
	updating.Status = newStatus

	if reflect.DeepEqual(prevStatus, updating.Status) {
		msg := fmt.Sprintf("addon %s/%s status the same. skip update.", updating.Namespace, updating.Name)
		c.logger.Info(msg)
		return nil
	}

	_, err = c.addoncli.AddonmgrV1alpha1().Addons(updating.Namespace).UpdateStatus(ctx, updating, metav1.UpdateOptions{})
	if err != nil {
		switch {
		case errors.IsNotFound(err):
			msg := fmt.Sprintf("Addon %s/%s is not found. %v", updating.Namespace, updating.Name, err)
			c.logger.Error(msg)
			return fmt.Errorf(msg)
		case strings.Contains(err.Error(), "the object has been modified"):
			c.logger.Info("retry updating object")
			if err := c.updateAddonStatusLifecycle(ctx, namespace, name, lifecycle, lifecyclestatus); err != nil {
				c.logger.Error("failed updating %s/%s status %#v", updating.Namespace, updating.Name, err)
				return err
			}
		default:
			c.logger.Error("failed updating %s/%s status %#v", updating.Namespace, updating.Name, err)
			return err
		}
	}
	msg := fmt.Sprintf("successfully update addon %s/%s step %s status to %s", namespace, name, lifecycle, lifecyclestatus)
	c.logger.Info(msg)
	return nil
}

func (c *Controller) updateAddonStatus(ctx context.Context, updating *addonv1.Addon) error {
	key := fmt.Sprintf("%s/%s", updating.Namespace, updating.Name)
	latest, err := c.getAddon(key)
	if err != nil {
		c.logger.Error("failed getting addon %s err %#v", key, err)
		return err
	}
	if reflect.DeepEqual(latest.Status, updating.Status) {
		c.logger.Info("addon %s status is the same. skip updating.", key)
		return nil
	}

	_, err = c.addoncli.AddonmgrV1alpha1().Addons(updating.Namespace).UpdateStatus(ctx, updating, metav1.UpdateOptions{})
	if err != nil {
		switch {
		case errors.IsNotFound(err):
			msg := fmt.Sprintf("Addon %s/%s is not found. %v", updating.Namespace, updating.Name, err)
			c.logger.Error(msg)
			return fmt.Errorf(msg)
		case strings.Contains(err.Error(), "the object has been modified"):
			c.logger.Info("retry updating object")
			if err := c.updateAddonStatus(ctx, updating); err != nil {
				c.logger.Error("failed updating %s/%s status %#v", updating.Namespace, updating.Name, err)
				return err
			}
		default:
			c.logger.Error("failed updating %s/%s status %#v", updating.Namespace, updating.Name, err)
			return err
		}
	}
	msg := fmt.Sprintf("successfully updated addon %s status", key)
	c.logger.Info(msg)
	return nil
}

func (c *Controller) createAddon(ctx context.Context, addon *addonv1.Addon, wfl workflows.AddonLifecycle) error {

	errors := []error{}

	_, addon.Status.Checksum = c.validateChecksum(addon)
	c.logger.Info(" init addon ", addon.Namespace, "/", addon.Name, " status")
	addon.Status = addonv1.AddonStatus{
		StartTime: common.GetCurretTimestamp(),
		Lifecycle: addonv1.AddonStatusLifecycle{
			Prereqs:   "",
			Installed: "",
		},
		Reason:    "",
		Resources: make([]addonv1.ObjectStatus, 0),
	}

	// Set finalizer only after addon is valid
	if err := c.SetFinalizer(ctx, addon, addonapiv1.FinalizerName); err != nil {
		reason := fmt.Sprintf("Addon %s/%s could not add finalizer. %v", addon.Namespace, addon.Name, err)
		c.recorder.Event(addon, "Warning", "Failed", reason)
		c.logger.Error(reason)
		addon.Status.Lifecycle.Installed = addonv1.Failed
		addon.Status.Reason = reason
		if err := c.updateAddon(ctx, addon); err != nil {
			c.logger.Error("Failed updating %s/%s finalizer error status %v", addon.Namespace, addon.Name, err)
			return err
		}
	}

	err := c.executePrereqAndInstall(ctx, addon, wfl)
	if err != nil {
		msg := fmt.Sprintf("\n failed installing addon %s/%s prereqs and instll err %v\n", addon.Namespace, addon.Name, err)
		c.logger.Error(msg)
		errors = append(errors, err)
	} else {
		c.recorder.Event(addon, "Normal", "Completed", fmt.Sprintf("Addon %s/%s workflow is executed.", addon.Namespace, addon.Name))
		c.logger.Info("workflow installation completed. waiting for update.")
		return nil
	}

	msg := fmt.Sprintf("failed install %s/%s err %v", addon.Namespace, addon.Name, errors)
	c.logger.Error(msg)
	return fmt.Errorf(msg)
}

func (c *Controller) executePrereqAndInstall(ctx context.Context, addon *addonv1.Addon, wfl workflows.AddonLifecycle) error {

	prereqsPhase, err := c.runWorkflow(addonv1.Prereqs, addon, wfl)
	if err != nil {
		reason := fmt.Sprintf("Addon %s/%s prereqs failed. %v", addon.Namespace, addon.Name, err)
		c.recorder.Event(addon, "Warning", "Failed", reason)
		c.logger.Error(err, "Addon prereqs workflow failed.")
		// if prereqs failed, set install status to failed as well so that STATUS is updated
		addon.Status.Lifecycle.Installed = addonv1.Failed
		addon.Status.Reason = reason
		return err
	}
	addon.Status.Lifecycle.Prereqs = prereqsPhase

	if err := c.validateSecrets(ctx, addon); err != nil {
		reason := fmt.Sprintf("Addon %s/%s could not validate secrets. %v", addon.Namespace, addon.Name, err)
		c.recorder.Event(addon, "Warning", "Failed", reason)
		c.logger.Error(err, "Addon could not validate secrets.")
		addon.Status.Lifecycle.Installed = addonv1.Failed
		addon.Status.Reason = reason
		return err
	}

	phase, err := c.runWorkflow(addonv1.Install, addon, wfl)
	addon.Status.Lifecycle.Installed = phase
	if err != nil {
		reason := fmt.Sprintf("Addon %s/%s could not be installed due to error. %v", addon.Namespace, addon.Name, err)
		c.recorder.Event(addon, "Warning", "Failed", reason)
		c.logger.Error(err, "Addon install workflow failed.")
		addon.Status.Reason = reason
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

func (c *Controller) addAddonToCache(addon *addonv1.Addon) {
	var version = addoninternal.Version{
		Name:        addon.GetName(),
		Namespace:   addon.GetNamespace(),
		PackageSpec: addon.GetPackageSpec(),
		PkgPhase:    addon.GetInstallStatus(),
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
func (c *Controller) validateChecksum(addon *addonv1.Addon) (bool, string) {
	newCheckSum := addon.CalculateChecksum()

	if addon.Status.Checksum == newCheckSum {
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
			if err := c.updateAddon(ctx, addon); err != nil {
				c.logger.Error("failed setting addon %s/%s finalizer, err %#v", addon.Namespace, addon.Name, err)
				return err
			}
		}
	}

	return nil
}

func (c *Controller) updateAddon(ctx context.Context, updated *addonv1.Addon) error {
	latest, err := c.addoncli.AddonmgrV1alpha1().Addons(updated.Namespace).Get(ctx, updated.Name, metav1.GetOptions{})
	if err != nil {
		msg := fmt.Sprintf("failed getting addon %s err %#v", updated.Name, err)
		c.logger.Error(msg)
		return fmt.Errorf(msg)
	}

	if reflect.DeepEqual(updated, latest) {
		c.logger.Info("latest and updated addon is the same, skip updating")
		return nil
	}
	_, err = c.addoncli.AddonmgrV1alpha1().Addons(updated.Namespace).Update(ctx, updated,
		metav1.UpdateOptions{})
	if err != nil {
		msg := fmt.Sprintf("Failed updating addon %s/%s. err %v", updated.Namespace, updated.Name, err)
		fmt.Print(msg)
		return fmt.Errorf(msg)
	}
	return nil
}

// Finalize runs finalizer for addon
func (c *Controller) Finalize(ctx context.Context, addon *addonv1.Addon, wfl workflows.AddonLifecycle, finalizerName string) error {
	// Has Delete workflow defined, let's run it.
	var removeFinalizer = true

	if addon.Spec.Lifecycle.Delete.Template != "" {
		removeFinalizer = false
		// Run delete workflow
		phase, err := c.runWorkflow(addonv1.Delete, addon, wfl)
		if err != nil {
			return err
		}

		if phase == addonv1.Succeeded || phase == addonv1.Failed {
			removeFinalizer = true
		}
	}

	// Remove version from cache
	c.versionCache.RemoveVersion(addon.Spec.PkgName, addon.Spec.PkgVersion)

	// Remove finalizer from the list and update it.
	if removeFinalizer && common.ContainsString(addon.ObjectMeta.Finalizers, finalizerName) {
		addon.ObjectMeta.Finalizers = common.RemoveString(addon.ObjectMeta.Finalizers, finalizerName)
		if err := c.updateAddon(ctx, addon); err != nil {
			c.logger.Error("failed removing addon %s/%s finalizer, err %#v", err)
			return err
		}
	}

	return nil
}
