// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: MPL-2.0

package datacenter

import (
	"context"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/terraform-provider-vsphere/vsphere/internal/helper/folder"
	"github.com/vmware/terraform-provider-vsphere/vsphere/internal/helper/provider"
)

// FromPath returns a Datacenter via its supplied path.
func FromPath(client *govmomi.Client, path string) (*object.Datacenter, error) {
	finder := find.NewFinder(client.Client, false)

	ctx, cancel := context.WithTimeout(context.Background(), provider.DefaultAPITimeout)
	defer cancel()
	return finder.Datacenter(ctx, path)
}

// FromInventoryPath returns the Datacenter object which is part of a given InventoryPath
func FromInventoryPath(client *govmomi.Client, inventoryPath string) (*object.Datacenter, error) {
	dcPath, err := folder.RootPathParticleDatastore.SplitDatacenter(inventoryPath)
	if err != nil {
		return nil, err
	}
	dc, err := FromPath(client, dcPath)
	if err != nil {
		return nil, err
	}

	return dc, nil
}
