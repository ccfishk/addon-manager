package controllers

import (
	"context"
	"fmt"
	"strings"

	addonv1 "github.com/keikoproj/addon-manager/api/addon/v1alpha1"
	"github.com/keikoproj/addon-manager/pkg/common"
	"github.com/keikoproj/addon-manager/pkg/workflows"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	addonapiv1 "github.com/keikoproj/addon-manager/api/addon"
	addoninternal "github.com/keikoproj/addon-manager/pkg/addon"
)

func (c *Controller) handleAddonCreation(ctx context.Context, addon *addonv1.Addon) error {
	c.logger.Info("creating addon ", addon.Namespace, "/", addon.Name)

	var wfl = workflows.NewWorkflowLifecycle(c.wfcli, c.informer, c.dynCli, addon, c.scheme, c.recorder)

	err := c.createAddon(ctx, addon, wfl)
	if err != nil {
		c.logger.Error("failed creating addon err :", err)
		return err
	}

	c.addAddonToCache(addon)

	return nil
}

func (c *Controller) handleAddonUpdate(ctx context.Context, addon *addonv1.Addon) error {
	c.logger.Info("updating addon ", addon.Namespace, "/", addon.Name)

	if !addon.ObjectMeta.DeletionTimestamp.IsZero() && addon.Status.Lifecycle.Installed != addonv1.Deleting {
		var wfl = workflows.NewWorkflowLifecycle(c.wfcli, c.informer, c.dynCli, addon, c.scheme, c.recorder)

		err := c.Finalize(ctx, addon, wfl)
		if err != nil {
			reason := fmt.Sprintf("Addon %s/%s could not be finalized. err %v", addon.Namespace, addon.Name, err)
			c.recorder.Event(addon, "Warning", "Failed", reason)
			addon.Status.Lifecycle.Installed = addonv1.DeleteFailed
			addon.Status.Reason = reason
			c.logger.Error(reason)
			if err := c.updateAddonStatus(ctx, addon); err != nil {
				c.logger.Error("failed updating ", addon.Namespace, "/", addon.Name, " finalizing status ", err)
				return err
			}
			return nil
		}

		addon.Status.Lifecycle.Installed = addonv1.Deleting
		if err := c.updateAddonStatus(ctx, addon); err != nil {
			c.logger.Error("failed updating ", addon.Namespace, "/", addon.Name, " deleting status ", err)
			return err
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

	var changedStatus bool
	changedStatus, addon.Status.Checksum = c.validateChecksum(addon)
	if changedStatus {
		c.logger.Info("addon ", addon.Namespace, "/", addon.Name, " spec checksum changes.")
		// Set ttl starttime if checksum has changed
		addon.Status.StartTime = common.GetCurretTimestamp()

		// Clear out status and reason
		addon.Status.Lifecycle.Prereqs = ""
		addon.Status.Lifecycle.Installed = ""
		addon.Status.Reason = ""
		addon.Status.Resources = []addonv1.ObjectStatus{}

		if err := c.updateAddonStatus(ctx, addon); err != nil {
			c.logger.Error("failed reset addon status ", addon.Namespace, "/", addon.Name, " after spec change.", err)
			return err
		}

		c.logger.Info(" re install addon after spec changes.")
		err := c.handleAddonCreation(ctx, addon)
		if err != nil {
			c.logger.Error("failed re-install addon ", addon.Namespace, "/", addon.Name, " after spec change.", err)
			return err
		}
	}

	return nil
}

func (c *Controller) handleAddonDeletion(ctx context.Context, addon *addonv1.Addon) error {
	c.logger.Info("deleting addon ", addon.Namespace, "/", addon.Name)
	if !addon.ObjectMeta.DeletionTimestamp.IsZero() {
		var wfl = workflows.NewWorkflowLifecycle(c.wfcli, c.informer, c.dynCli, addon, c.scheme, c.recorder)

		err := c.Finalize(ctx, addon, wfl)
		if err != nil {
			reason := fmt.Sprintf("Addon %s/%s could not be finalized. %v", addon.Namespace, addon.Name, err)
			c.recorder.Event(addon, "Warning", "Failed", reason)
			addon.Status.Lifecycle.Installed = addonv1.DeleteFailed
			addon.Status.Reason = reason
			if err := c.updateAddonStatus(ctx, addon); err != nil {
				c.logger.Error("failed updating ", addon.Namespace, "/", addon.Name, " finalizing failure ", err)
				return err
			}
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

func (c *Controller) createAddon(ctx context.Context, addon *addonv1.Addon, wfl workflows.AddonLifecycle) error {
	c.logger.Info("processing addon ", addon.Namespace, "/", addon.Name, " status")

	errors := []error{}
	addon.Status = addonv1.AddonStatus{
		StartTime: common.GetCurretTimestamp(),
		Lifecycle: addonv1.AddonStatusLifecycle{
			Prereqs:   "",
			Installed: "",
		},
		Reason:    "",
		Resources: make([]addonv1.ObjectStatus, 0),
	}
	_, addon.Status.Checksum = c.validateChecksum(addon)

	// Set finalizer only after addon is valid
	if err := c.SetFinalizer(ctx, addon, addonapiv1.FinalizerName); err != nil {
		reason := fmt.Sprintf("Addon %s/%s could not add finalizer. %v", addon.Namespace, addon.Name, err)
		c.recorder.Event(addon, "Warning", "Failed", reason)
		c.logger.Error(reason)
		addon.Status.Lifecycle.Installed = addonv1.Failed
		addon.Status.Reason = reason
		if err := c.updateAddon(ctx, addon); err != nil {
			c.logger.Error("Failed updating ", addon.Namespace, "/", addon.Name, " finalizer error status err ", err)
			return err
		}
	}

	if addon.Spec.Lifecycle.Prereqs.Template != "" || addon.Spec.Lifecycle.Install.Template != "" {
		err := c.executePrereqAndInstall(ctx, addon, wfl)
		if err != nil {
			msg := fmt.Sprintf("failed installing addon %s/%s prereqs and instll err %v", addon.Namespace, addon.Name, err)
			c.logger.Error(msg)
			errors = append(errors, err)
		}
		msg := fmt.Sprintf("failed install %s/%s err %v", addon.Namespace, addon.Name, errors)
		c.logger.Error(msg)
		return fmt.Errorf(msg)
	} else {
		c.logger.Info("addon ", addon.Namespace, "/", addon.Name, " does not need prereqs or install.")
	}

	if addon.Spec.Lifecycle.Delete.Template != "" {
		c.logger.Info("addon ", addon.Namespace, "/", addon.Name, " has delete template.")
		_, err := c.runWorkflow(addonv1.Delete, addon, wfl)
		if err != nil {
			c.logger.Error(" failed kick off delete workflow ", err)
			return err
		}
	}

	c.recorder.Event(addon, "Normal", "Completed", fmt.Sprintf("Addon %s/%s workflow is executed.", addon.Namespace, addon.Name))
	c.logger.Info("workflow installation completed. waiting for update.")

	return nil
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
				c.logger.Error("failed setting addon ", addon.Namespace, addon.Name, " finalizer, err :", err)
				return err
			}
		}
	}

	return nil
}

// Finalize runs finalizer for addon
func (c *Controller) Finalize(ctx context.Context, addon *addonv1.Addon, wfl workflows.AddonLifecycle) error {
	if addon.Spec.Lifecycle.Delete.Template != "" {
		_, err := c.runWorkflow(addonv1.Delete, addon, wfl)
		if err != nil {
			return err
		}
	}

	// Remove version from cache
	c.versionCache.RemoveVersion(addon.Spec.PkgName, addon.Spec.PkgVersion)
	return nil
}

func (c *Controller) removeFinalizer(addon *addonv1.Addon) {
	if common.ContainsString(addon.ObjectMeta.Finalizers, addonapiv1.FinalizerName) {
		addon.ObjectMeta.Finalizers = common.RemoveString(addon.ObjectMeta.Finalizers, addonapiv1.FinalizerName)
	}
}
