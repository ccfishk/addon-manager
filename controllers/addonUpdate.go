package controllers

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	wfv1 "github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
	addonv1 "github.com/keikoproj/addon-manager/pkg/apis/addon/v1alpha1"
	"github.com/keikoproj/addon-manager/pkg/common"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

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

	if lifecycle == "delete" && addonv1.ApplicationAssemblyPhase(lifecyclestatus).Succeeded() {
		c.logger.Info("addon", namespace, "/", name, "deletion wf succeeded remove its finalizer for cleanup")
		c.removeFinalizer(updating)
		_, err = c.addoncli.AddonmgrV1alpha1().Addons(updating.Namespace).Update(ctx, updating, metav1.UpdateOptions{})
		if err != nil {
			switch {
			case errors.IsNotFound(err):
				msg := fmt.Sprintf("Addon %s/%s is not found. %v", updating.Namespace, updating.Name, err)
				c.logger.Error(msg)
				return fmt.Errorf(msg)
			case strings.Contains(err.Error(), "the object has been modified"):
				c.logger.Info("retry updating object")
				if _, err := c.addoncli.AddonmgrV1alpha1().Addons(updating.Namespace).Update(ctx, updating, metav1.UpdateOptions{}); err != nil {
					c.logger.Error("failed updating %s/%s err %#v", updating.Namespace, updating.Name, err)
					return err
				}
			default:
				c.logger.Error("failed updating %s/%s serr %#v", updating.Namespace, updating.Name, err)
				return err
			}
		}
	} else {
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
					c.logger.Error("retry failed updating ", updating.Namespace, "/", updating.Name, " status ", err)
					return err
				}
			default:
				c.logger.Error("failed updating ", updating.Namespace, "/", updating.Name, " status ", err)
				return err
			}
		}
		msg := fmt.Sprintf("successfully update addon %s/%s step %s status to %s", namespace, name, lifecycle, lifecyclestatus)
		c.logger.Info(msg)
	}
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
				c.logger.Error("failed updating ", updating.Namespace, updating.Name, " status ", err)
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

func (c *Controller) updateAddonStatusOnly(ctx context.Context, updated *addonv1.Addon) error {
	_, err := c.addoncli.AddonmgrV1alpha1().Addons(updated.Namespace).UpdateStatus(ctx, updated,
		metav1.UpdateOptions{})
	if err != nil {
		msg := fmt.Sprintf("failed updating addon %s/%s status. err %v", updated.Namespace, updated.Name, err)
		c.logger.Error(msg)
		return fmt.Errorf(msg)
	}
	return nil
}

func (c *Controller) updateAddonStatusResources(ctx context.Context, key string, resource addonv1.ObjectStatus) error {
	addon, err := c.getAddon(key)
	if err != nil {
		msg := fmt.Sprintf("failed finding addon %s err %v.", key, err)
		c.logger.Error(msg)
		return fmt.Errorf(msg)
	}

	updating := addon.DeepCopy()

	fmt.Printf("\n existing resources %#v\n", addon.Status.Resources)
	newResources := []addonv1.ObjectStatus{resource}
	newResources = append(newResources, addon.Status.Resources...)
	updating.Status.Resources = newResources

	fmt.Printf("\n updated resources %#v\n", updating.Status.Resources)
	return c.updateAddonStatusOnly(ctx, addon)
}
