// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testexecution

import (
	"fmt"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/components"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
)

// FindComponent resolves a test subject without requiring packaging lock consistency.
func FindComponent(env *azldev.Env, name string) (*components.ComponentSet, error) {
	set, err := components.NewResolver(env).FindComponents(&components.ComponentFilter{
		ComponentNamePatterns: []string{name}, SkipLockValidation: true,
	})
	if err != nil {
		return nil, fmt.Errorf("find test component:\n%w", err)
	}

	if set.Len() != 1 {
		return nil, fmt.Errorf("expected exactly one component named %#q, found %d", name, set.Len())
	}

	return set, nil
}

// FindImage accepts project images and published-only images without requiring a build recipe.
func FindImage(env *azldev.Env, name string) (projectconfig.ImageConfig, error) {
	if image, exists := env.Config().Images[name]; exists {
		return image, nil
	}

	_, distro, err := env.Distro()
	if err != nil {
		return projectconfig.ImageConfig{}, fmt.Errorf("find test image:\n%w", err)
	}

	if _, exists := distro.Images[name]; !exists {
		return projectconfig.ImageConfig{}, fmt.Errorf("image %#q is not defined locally or in the published catalog", name)
	}

	return projectconfig.ImageConfig{Name: name}, nil
}
