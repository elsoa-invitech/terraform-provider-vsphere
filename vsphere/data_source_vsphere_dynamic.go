// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: MPL-2.0

package vsphere

import (
	"context"
	"fmt"
	"log"
	"regexp"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vapi/tags"
)

func dataSourceVSphereDynamic() *schema.Resource {
	return &schema.Resource{
		Read: dataSourceVSphereDynamicRead,

		Schema: map[string]*schema.Schema{
			"filter": {
				Type:        schema.TypeSet,
				Required:    true,
				Description: "List of tag IDs to match target.",
				Elem:        &schema.Schema{Type: schema.TypeString},
			},
			"name_regex": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "A regular expression used to match against managed object names.",
			},
			"type": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The type of managed object to return.",
			},
		},
	}
}

func dataSourceVSphereDynamicRead(d *schema.ResourceData, meta interface{}) error {
	log.Printf("[DEBUG] dataSourceDynamic: Beginning dynamic data source read.")
	tm, err := meta.(*Client).TagsManager()
	if err != nil {
		return err
	}
	tagIDs := d.Get("filter").(*schema.Set).List()
	matches, err := filterObjectsByTag(tm, tagIDs)
	if err != nil {
		return err
	}
	filtered, err := filterObjectsByName(d, meta, matches)
	if err != nil {
		return err
	}
	switch {
	case len(filtered) < 1:
		return fmt.Errorf("no matching resources found")
	case len(filtered) > 1:
		log.Printf("dataSourceVSphereDynamic: Multiple matches found: %v", filtered)
		return fmt.Errorf("multiple objects match the supplied criteria")
	}
	d.SetId(filtered[0])
	log.Printf("[DEBUG] dataSourceDynamic: Read complete. Resource located: %s", filtered[0])
	return nil
}

func filterObjectsByName(d *schema.ResourceData, meta interface{}, matches []tags.AttachedObjects) ([]string, error) {
	log.Printf("[DEBUG] dataSourceDynamic: Filtering objects by name.")
	var filtered []string
	re, err := regexp.Compile(d.Get("name_regex").(string))
	if err != nil {
		return nil, err
	}
	for _, match := range matches[0].ObjectIDs {
		mtype := d.Get("type").(string)
		if mtype != "" && match.Reference().Type != mtype {
			// Skip this object because the type does not match
			continue
		}
		attachedObject := object.NewCommon(meta.(*Client).vimClient.Client, match.Reference())
		name, err := attachedObject.ObjectName(context.TODO())
		if err != nil {
			return nil, err
		}
		if re.Match([]byte(name)) {
			log.Printf("[DEBUG] dataSourceDynamic: Match found: %s", name)
			filtered = append(filtered, match.Reference().Value)
		}
	}
	return filtered, nil
}

func filterObjectsByTag(tm *tags.Manager, t []interface{}) ([]tags.AttachedObjects, error) {
	log.Printf("[DEBUG] dataSourceDynamic: Filtering objects by tags.")
	var tagIDs []string
	for _, ti := range t {
		tagIDs = append(tagIDs, ti.(string))
	}
	matches, err := tm.GetAttachedObjectsOnTags(context.TODO(), tagIDs)
	if err != nil {
		return nil, err
	}
	for _, match := range matches {
		matches[0] = attachedObjectsIntersection(matches[0], match)
	}
	if len(matches[0].ObjectIDs) < 1 {
		return nil, fmt.Errorf("no resources match filter")
	}
	log.Printf("[DEBUG] dataSourceDynamic: Objects filtered.")
	return matches, nil
}

func attachedObjectsIntersection(a, b tags.AttachedObjects) tags.AttachedObjects {
	var inter tags.AttachedObjects
	for _, aval := range a.ObjectIDs {
		for _, bval := range b.ObjectIDs {
			if aval == bval {
				inter.ObjectIDs = append(inter.ObjectIDs, aval)
			}
		}
	}
	return inter
}
