package controllers

import (
	"context"
	"fmt"
)

func (c *Controller) handleNamespaceAdd(ctx context.Context, obj interface{}) error {
	fmt.Printf("\n yes, detect ns creation. update object status \n")
	return nil
}

func (c *Controller) handleNamespaceUpdate(ctx context.Context, obj interface{}) error {
	fmt.Printf("\n yes, detect ns updating. update object status \n")
	return nil
}

func (c *Controller) handleNamespaceDeletion(ctx context.Context, obj interface{}) error {
	fmt.Printf("\n yes, detect ns deletion. update object status \n")
	return nil
}

func (c *Controller) handleDeploymentAdd(ctx context.Context, obj interface{}) error {
	fmt.Printf("\n yes, detect deployment add. update object status \n")
	return nil
}

func (c *Controller) handleDeploymentUpdate(ctx context.Context, obj interface{}) error {
	fmt.Printf("\n yes, detect deployment update. update object status \n")
	return nil
}

func (c *Controller) handleDeploymentDeletion(ctx context.Context, obj interface{}) error {
	fmt.Printf("\n yes, detect deployment deletion. update object status \n")
	return nil
}
