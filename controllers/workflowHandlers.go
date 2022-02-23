package controllers

import (
	"context"
	"fmt"

	wfv1 "github.com/argoproj/argo-workflows/v3/pkg/apis/workflow/v1alpha1"
	addonwfutility "github.com/keikoproj/addon-manager/pkg/workflows"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func (c *Controller) handleWorkFlowUpdate(ctx context.Context, obj interface{}) error {
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

func (c *Controller) handleWorkFlowAdd(ctx context.Context, obj interface{}) error {
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

func (c *Controller) handleWorkFlowDelete(ctx context.Context, newEvent Event) error {
	fmt.Printf("\n yes, detect wf deletion \n")
	key := newEvent.key
	fmt.Printf("\n looking for key %s\n", key)
	item, found, err := c.wfinformer.GetIndexer().GetByKey(newEvent.key)
	if err != nil {
		msg := fmt.Sprintf("retrieving wf %s err %v", key, err)
		fmt.Print(msg)
		return fmt.Errorf(msg)
	} else if !found {
		msg := fmt.Sprintf("failed finding %s err %v", key, err)
		fmt.Print(msg)
		return fmt.Errorf(msg)
	}
	wf := item.(*unstructured.Unstructured)
	fmt.Printf("\n wf %s %s\n", wf.GetNamespace(), wf.GetName())
	return nil
}
