package controllers

import (
	"context"
	"fmt"

	addonapiv1 "github.com/keikoproj/addon-manager/pkg/apis/addon"
	addonv1 "github.com/keikoproj/addon-manager/pkg/apis/addon/v1alpha1"
	"github.com/keikoproj/addon-manager/pkg/common"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func (c *Controller) handleNamespaceAdd(ctx context.Context, obj interface{}) error {
	ns, ok := obj.(*v1.Namespace)
	if !ok {
		msg := "expecting v1 namespace."
		c.logger.Error(msg)
		return fmt.Errorf(msg)
	}
	addonName, ok := ns.GetLabels()[addonapiv1.ResourceDefaultOwnLabel]
	if !ok {
		c.logger.Info("owner is not set. checking part of")
		addonName, ok = ns.GetLabels()[addonapiv1.ResourceDefaultPartLabel]
		if !ok {
			msg := "failed getting addon name, should not happen"
			c.logger.Error(msg)
			return fmt.Errorf(msg)
		}
	}
	key := fmt.Sprintf("%s/%s", c.namespace, addonName)
	nsStatus := addonv1.ObjectStatus{
		Kind:  "Namespace",
		Group: "",
		Name:  ns.GetName(),
		Link:  ns.GetSelfLink(),
	}

	err := c.updateAddonStatusResources(ctx, key, nsStatus)
	if err != nil {
		c.logger.Error("failed updating ", key, " resource status.  err : ", err)
	}
	return nil
}

func (c *Controller) handleNamespaceUpdate(ctx context.Context, obj interface{}) error {
	return nil
}

func (c *Controller) handleNamespaceDeletion(ctx context.Context, obj interface{}) error {
	return nil
}

func (c *Controller) handleDeploymentAdd(ctx context.Context, obj interface{}) error {
	deploy, ok := obj.(*appsv1.Deployment)
	if !ok {
		msg := "expecting appsv1 deployment."
		c.logger.Error(msg)
		return fmt.Errorf(msg)
	}
	addonName, ok := deploy.GetLabels()[addonapiv1.ResourceDefaultOwnLabel]
	if !ok {
		c.logger.Info("owner is not set. checking part of")
		addonName, ok = deploy.GetLabels()[addonapiv1.ResourceDefaultPartLabel]
		if !ok {
			msg := "failed getting addon name, should not happen"
			c.logger.Error(msg)
			return fmt.Errorf(msg)
		}
	}
	nsStatus := addonv1.ObjectStatus{
		Kind:  "Namespace",
		Group: "",
		Name:  deploy.GetName(),
		Link:  deploy.GetSelfLink(),
	}
	key := fmt.Sprintf("%s/%s", c.namespace, addonName)

	err := c.updateAddonStatusResources(ctx, key, nsStatus)
	if err != nil {
		c.logger.Error("failed updating ", key, " resource status. err:", err)
	}
	return nil
}

func (c *Controller) handleDeploymentUpdate(ctx context.Context, obj interface{}) error {
	return nil
}

func (c *Controller) handleDeploymentDeletion(ctx context.Context, obj interface{}) error {
	return nil
}

func (c *Controller) updateAddonStatusResources(ctx context.Context, key string, resource addonv1.ObjectStatus) error {
	un, found, err := c.informer.GetIndexer().GetByKey(key)
	if err != nil || !found {
		msg := fmt.Sprintf("failed finding %s err %v", key, err)
		c.logger.Info(msg)
		return fmt.Errorf(msg)
	}
	addon, err := common.FromUnstructured(un.(*unstructured.Unstructured))
	if err != nil || !found {
		msg := fmt.Sprintf("failed converting addon %s err %v", key, err)
		c.logger.Error(msg)
		return fmt.Errorf(msg)
	}

	newStatus := []addonv1.ObjectStatus{resource}
	newStatus = append(newStatus, addon.Status.Resources...)
	addon.Status.Resources = newStatus
	err = c.updateAddon(ctx, addon)
	if err != nil {
		msg := fmt.Sprintf("failed updating addon %s  resources err %v", key, err)
		c.logger.Error(msg)
		return fmt.Errorf(msg)
	}
	return nil
}
