package controllers

import (
	"context"
	"fmt"

	addonapiv1 "github.com/keikoproj/addon-manager/pkg/apis/addon"
	addonv1 "github.com/keikoproj/addon-manager/pkg/apis/addon/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbac_v1 "k8s.io/api/rbac/v1"
)

// attention: start/re-start, filter existing already processed resources
func (c *Controller) handleNamespaceAdd(ctx context.Context, obj interface{}) error {
	ns, ok := obj.(*v1.Namespace)
	if !ok {
		msg := "expecting v1 namespace"
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
		c.logger.Error("expecting appsv1 deployment")
		return fmt.Errorf("expecting appsv1 deployment")
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
		Kind:  "Deployment",
		Group: "apps/v1",
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

// attention: start/re-start, filter existing already processed resources
func (c *Controller) handleServiceAccountAdd(ctx context.Context, obj interface{}) error {
	srvacnt, ok := obj.(*v1.ServiceAccount)
	if !ok {
		msg := "expecting v1 ServiceAccount"
		c.logger.Error(msg)
		return fmt.Errorf(msg)
	}
	addonName, ok := srvacnt.GetLabels()[addonapiv1.ResourceDefaultOwnLabel]
	if !ok {
		c.logger.Info("owner is not set. checking part of")
		addonName, ok = srvacnt.GetLabels()[addonapiv1.ResourceDefaultPartLabel]
		if !ok {
			msg := "failed getting addon name, should not happen"
			c.logger.Error(msg)
			return fmt.Errorf(msg)
		}
	}
	key := fmt.Sprintf("%s/%s", c.namespace, addonName)
	nsStatus := addonv1.ObjectStatus{
		Kind:  "ServiceAccount",
		Group: "v1",
		Name:  srvacnt.GetName(),
		Link:  srvacnt.GetSelfLink(),
	}
	err := c.updateAddonStatusResources(ctx, key, nsStatus)
	if err != nil {
		c.logger.Error("failed updating ", key, " resource status.  err : ", err)
	}
	return nil
}

func (c *Controller) handleServiceAccountUpdate(ctx context.Context, obj interface{}) error {
	return nil
}

func (c *Controller) handleServiceAccountDeletion(ctx context.Context, obj interface{}) error {
	return nil
}

// attention: start/re-start, filter existing already processed resources
func (c *Controller) handleConfigMapAdd(ctx context.Context, obj interface{}) error {
	configmap, ok := obj.(*v1.ConfigMap)
	if !ok {
		msg := "expecting v1 ConfigMap"
		c.logger.Error(msg)
		return fmt.Errorf(msg)
	}
	addonName, ok := configmap.GetLabels()[addonapiv1.ResourceDefaultOwnLabel]
	if !ok {
		c.logger.Info("owner is not set. checking part of")
		addonName, ok = configmap.GetLabels()[addonapiv1.ResourceDefaultPartLabel]
		if !ok {
			msg := "failed getting addon name, should not happen"
			c.logger.Error(msg)
			return fmt.Errorf(msg)
		}
	}
	key := fmt.Sprintf("%s/%s", c.namespace, addonName)
	nsStatus := addonv1.ObjectStatus{
		Kind:  "ConfigMap",
		Group: "v1",
		Name:  configmap.GetName(),
		Link:  configmap.GetSelfLink(),
	}
	err := c.updateAddonStatusResources(ctx, key, nsStatus)
	if err != nil {
		c.logger.Error("failed updating ", key, " resource status.  err : ", err)
	}
	return nil
}

func (c *Controller) handleConfigMapUpdate(ctx context.Context, obj interface{}) error {
	return nil
}

func (c *Controller) handleConfigMapDeletion(ctx context.Context, obj interface{}) error {
	return nil
}

// attention: start/re-start, filter existing already processed resources
func (c *Controller) handleClusterRoleAdd(ctx context.Context, obj interface{}) error {
	clsRole, ok := obj.(*rbac_v1.ClusterRole)
	if !ok {
		msg := "expecting v1 ClusterRole"
		c.logger.Error(msg)
		return fmt.Errorf(msg)
	}
	addonName, ok := clsRole.GetLabels()[addonapiv1.ResourceDefaultOwnLabel]
	if !ok {
		c.logger.Info("owner is not set. checking part of")
		addonName, ok = clsRole.GetLabels()[addonapiv1.ResourceDefaultPartLabel]
		if !ok {
			msg := "failed getting addon name, should not happen"
			c.logger.Error(msg)
			return fmt.Errorf(msg)
		}
	}
	key := fmt.Sprintf("%s/%s", c.namespace, addonName)
	nsStatus := addonv1.ObjectStatus{
		Kind:  "ClusterRole",
		Group: "v1",
		Name:  clsRole.GetName(),
		Link:  clsRole.GetSelfLink(),
	}
	err := c.updateAddonStatusResources(ctx, key, nsStatus)
	if err != nil {
		c.logger.Error("failed updating ", key, " resource status.  err : ", err)
	}
	return nil
}

func (c *Controller) handleClusterRoleUpdate(ctx context.Context, obj interface{}) error {
	return nil
}

func (c *Controller) handleClusterRoleDeletion(ctx context.Context, obj interface{}) error {
	return nil
}

// attention: start/re-start, filter existing already processed resources
func (c *Controller) handleClusterRoleBindingAdd(ctx context.Context, obj interface{}) error {
	clsRoleBnd, ok := obj.(*rbac_v1.ClusterRoleBinding)
	if !ok {
		msg := "expecting v1 ClusterRole"
		c.logger.Error(msg)
		return fmt.Errorf(msg)
	}
	addonName, ok := clsRoleBnd.GetLabels()[addonapiv1.ResourceDefaultOwnLabel]
	if !ok {
		c.logger.Info("owner is not set. checking part of")
		addonName, ok = clsRoleBnd.GetLabels()[addonapiv1.ResourceDefaultPartLabel]
		if !ok {
			msg := "failed getting addon name, should not happen"
			c.logger.Error(msg)
			return fmt.Errorf(msg)
		}
	}
	key := fmt.Sprintf("%s/%s", c.namespace, addonName)
	nsStatus := addonv1.ObjectStatus{
		Kind:  "ClusterRole",
		Group: "v1",
		Name:  clsRoleBnd.GetName(),
		Link:  clsRoleBnd.GetSelfLink(),
	}
	err := c.updateAddonStatusResources(ctx, key, nsStatus)
	if err != nil {
		c.logger.Error("failed ClusterRoleBinding ", key, " resource status.  err : ", err)
	}
	return nil
}

func (c *Controller) handleClusterRoleBindingUpdate(ctx context.Context, obj interface{}) error {
	return nil
}

func (c *Controller) handleClusterRoleBindingDeletion(ctx context.Context, obj interface{}) error {
	return nil
}
