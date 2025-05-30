---
subcategory: "Inventory"
page_title: "VMware vSphere: vsphere_custom_attribute"
sidebar_current: "docs-vsphere-data-source-custom-attribute"
description: |-
  Provides a vSphere custom attribute data source. This can be used to
  reference custom attributes not managed in Terraform.
---

# vsphere_custom_attribute

The `vsphere_custom_attribute` data source can be used to reference custom
attributes that are not managed by Terraform. Its attributes are exactly the
same as the [`vsphere_custom_attribute` resource][resource-custom-attribute],
and, like importing, the data source takes a name argument for the search. The
`id` and other attributes are then populated with the data found by the search.

[resource-custom-attribute]: /docs/providers/vsphere/r/custom_attribute.html

~> **NOTE:** Custom attributes are unsupported on direct ESXi host connections
and require vCenter Server.

## Example Usage

```hcl
data "vsphere_custom_attribute" "attribute" {
  name = "terraform-test-attribute"
}
```

## Argument Reference

* `name` - (Required) The name of the custom attribute.

## Attribute Reference

In addition to the `id` being exported, all of the fields that are available in
the [`vsphere_custom_attribute` resource][resource-custom-attribute] are also
populated.
