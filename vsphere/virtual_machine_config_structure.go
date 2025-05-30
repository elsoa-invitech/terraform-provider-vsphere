// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: MPL-2.0

package vsphere

import (
	"fmt"
	"log"
	"reflect"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
	"github.com/mitchellh/copystructure"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/vim25/types"
	"github.com/vmware/terraform-provider-vsphere/vsphere/internal/helper/spbm"
	"github.com/vmware/terraform-provider-vsphere/vsphere/internal/helper/structure"
	"github.com/vmware/terraform-provider-vsphere/vsphere/internal/helper/viapi"
	"github.com/vmware/terraform-provider-vsphere/vsphere/internal/helper/virtualmachine"
)

var virtualMachineResourceAllocationTypeValues = []string{"cpu", "memory"}

var virtualMachineVirtualExecUsageAllowedValues = []string{
	string(types.VirtualMachineFlagInfoVirtualExecUsageHvAuto),
	string(types.VirtualMachineFlagInfoVirtualExecUsageHvOn),
	string(types.VirtualMachineFlagInfoVirtualExecUsageHvOff),
}

var virtualMachineVirtualMmuUsageAllowedValues = []string{
	string(types.VirtualMachineFlagInfoVirtualMmuUsageAutomatic),
	string(types.VirtualMachineFlagInfoVirtualMmuUsageOn),
	string(types.VirtualMachineFlagInfoVirtualMmuUsageOff),
}

var virtualMachineSwapPlacementAllowedValues = []string{
	string(types.VirtualMachineConfigInfoSwapPlacementTypeInherit),
	string(types.VirtualMachineConfigInfoSwapPlacementTypeVmDirectory),
	string(types.VirtualMachineConfigInfoSwapPlacementTypeHostLocal),
}

var virtualMachineUpgradePolicyAllowedValues = []string{
	string(types.UpgradePolicyManual),
	string(types.UpgradePolicyUpgradeAtPowerCycle),
}

var virtualMachineFirmwareAllowedValues = []string{
	string(types.GuestOsDescriptorFirmwareTypeBios),
	string(types.GuestOsDescriptorFirmwareTypeEfi),
}

var virtualMachineLatencySensitivityAllowedValues = []string{
	string(types.LatencySensitivitySensitivityLevelLow),
	string(types.LatencySensitivitySensitivityLevelNormal),
	string(types.LatencySensitivitySensitivityLevelMedium),
	string(types.LatencySensitivitySensitivityLevelHigh),
}

// getWithRestart fetches the resource data specified at key. If the value has
// changed, a reboot is flagged in the virtual machine by setting
// reboot_required to true.
func getWithRestart(d *schema.ResourceData, key string) interface{} {
	if d.HasChange(key) {
		log.Printf("[DEBUG] %s: Resource argument %q requires a VM restart", resourceVSphereVirtualMachineIDString(d), key)
		_ = d.Set("reboot_required", true)
	}
	return d.Get(key)
}

// getBoolWithRestart fetches a *bool for the resource data item specified at
// key. If the value has changed, a reboot is flagged in the virtual machine by
// setting reboot_required to true.
//
// This function always returns at least false, even if a value is unspecified.
func getBoolWithRestart(d *schema.ResourceData, key string) *bool {
	if d.HasChange(key) {
		_ = d.Set("reboot_required", true)
	}
	return structure.GetBool(d, key)
}

// schemaVirtualMachineConfigSpec returns schema items for resources that
// need to work with a VirtualMachineConfigSpec.
func schemaVirtualMachineConfigSpec() map[string]*schema.Schema {
	s := map[string]*schema.Schema{
		// VirtualMachineBootOptions
		"boot_delay": {
			Type:        schema.TypeInt,
			Optional:    true,
			Description: "The number of milliseconds to wait before starting the boot sequence.",
		},
		"efi_secure_boot_enabled": {
			Type:        schema.TypeBool,
			Optional:    true,
			Description: "When the boot type set in firmware is efi, this enables EFI secure boot.",
		},
		"boot_retry_delay": {
			Type:        schema.TypeInt,
			Optional:    true,
			Default:     10000,
			Description: "The number of milliseconds to wait before retrying the boot sequence. This only valid if boot_retry_enabled is true.",
		},
		"boot_retry_enabled": {
			Type:        schema.TypeBool,
			Optional:    true,
			Description: "If set to true, a virtual machine that fails to boot will try again after the delay defined in boot_retry_delay.",
		},

		// VirtualMachineFlagInfo
		"enable_disk_uuid": {
			Type:        schema.TypeBool,
			Optional:    true,
			Description: "Expose the UUIDs of attached virtual disks to the virtual machine, allowing access to them in the guest.",
		},
		"vbs_enabled": {
			Type:        schema.TypeBool,
			Optional:    true,
			Description: "Flag to specify if Virtualization-based security is enabled for this virtual machine.",
		},
		"vvtd_enabled": {
			Type:        schema.TypeBool,
			Optional:    true,
			Description: "Flag to specify if I/O MMU virtualization, also called Intel Virtualization Technology for Directed I/O (VT-d) and AMD I/O Virtualization (AMD-Vi or IOMMU), is enabled.",
		},
		"hv_mode": {
			Type:         schema.TypeString,
			Optional:     true,
			Computed:     true,
			Description:  "The (non-nested) hardware virtualization setting for this virtual machine. Can be one of hvAuto, hvOn, or hvOff.",
			ValidateFunc: validation.StringInSlice(virtualMachineVirtualExecUsageAllowedValues, false),
		},
		"ept_rvi_mode": {
			Type:         schema.TypeString,
			Optional:     true,
			Computed:     true,
			Description:  "The EPT/RVI (hardware memory virtualization) setting for this virtual machine. Can be one of automatic, on, or off.",
			ValidateFunc: validation.StringInSlice(virtualMachineVirtualMmuUsageAllowedValues, false),
		},
		"enable_logging": {
			Type:        schema.TypeBool,
			Optional:    true,
			Description: "Enable logging on this virtual machine.",
			DiffSuppressFunc: func(k, oldSetting, newSetting string, d *schema.ResourceData) bool {
				return len(d.Get("ovf_deploy").([]interface{})) > 0
			},
		},

		// ToolsConfigInfo
		"sync_time_with_host": {
			Type:        schema.TypeBool,
			Optional:    true,
			Default:     true,
			Description: "Enable guest clock synchronization with the host. On vSphere 7.0 U1 and above, with only this setting the clock is synchronized on startup and resume. Requires VMware Tools to be installed.",
		},
		"sync_time_with_host_periodically": {
			Type:        schema.TypeBool,
			Optional:    true,
			Description: "Enable periodic clock synchronization with the host. Supported only on vSphere 7.0 U1 and above. On prior versions setting `sync_time_with_host` is enough for periodic synchronization. Requires VMware Tools to be installed.",
		},
		"tools_upgrade_policy": {
			Type:         schema.TypeString,
			Optional:     true,
			Default:      string(types.UpgradePolicyManual),
			Description:  "Set the upgrade policy for VMware Tools. Can be one of `manual` or `upgradeAtPowerCycle`.",
			ValidateFunc: validation.StringInSlice(virtualMachineUpgradePolicyAllowedValues, false),
		},
		"run_tools_scripts_after_power_on": {
			Type:        schema.TypeBool,
			Optional:    true,
			Default:     true,
			Description: "Enable the run of scripts after virtual machine power-on when VMware Tools is installed.",
		},
		"run_tools_scripts_after_resume": {
			Type:        schema.TypeBool,
			Optional:    true,
			Default:     true,
			Description: "Enable the run of scripts after virtual machine resume when when VMware Tools is installed.",
		},
		"run_tools_scripts_before_guest_reboot": {
			Type:        schema.TypeBool,
			Optional:    true,
			Description: "Enable the run of scripts before guest operating system reboot when VMware Tools is installed.",
		},
		"run_tools_scripts_before_guest_shutdown": {
			Type:        schema.TypeBool,
			Optional:    true,
			Default:     true,
			Description: "Enable the run of scripts before guest operating system shutdown when VMware Tools is installed.",
		},
		"run_tools_scripts_before_guest_standby": {
			Type:        schema.TypeBool,
			Optional:    true,
			Default:     true,
			Description: "Enable the run of scripts before guest operating system standby when VMware Tools is installed.",
		},

		// LatencySensitivity
		"latency_sensitivity": {
			Type:         schema.TypeString,
			Optional:     true,
			Default:      types.LatencySensitivitySensitivityLevelNormal,
			Description:  "Controls the scheduling delay of the virtual machine. Use a higher sensitivity for applications that require lower latency, such as VOIP, media player applications, or applications that require frequent access to mouse or keyboard devices. Can be one of low, normal, medium, or high.",
			ValidateFunc: validation.StringInSlice(virtualMachineLatencySensitivityAllowedValues, false),
		},

		// VirtualMachineConfigSpec
		"name": {
			Type:         schema.TypeString,
			Required:     true,
			Description:  "The name of this virtual machine.",
			ValidateFunc: validation.StringLenBetween(1, 80),
		},
		"num_cpus": {
			Type:        schema.TypeInt,
			Optional:    true,
			Default:     1,
			Description: "The number of virtual processors to assign to this virtual machine.",
		},
		"num_cores_per_socket": {
			Type:        schema.TypeInt,
			Optional:    true,
			Default:     1,
			Description: "The number of cores to distribute amongst the CPUs in this virtual machine. If specified, the value supplied to num_cpus must be evenly divisible by this value.",
		},
		"cpu_hot_add_enabled": {
			Type:        schema.TypeBool,
			Optional:    true,
			Description: "Allow CPUs to be added to this virtual machine while it is running.",
		},
		"cpu_hot_remove_enabled": {
			Type:        schema.TypeBool,
			Optional:    true,
			Description: "Allow CPUs to be added to this virtual machine while it is running.",
		},
		"nested_hv_enabled": {
			Type:        schema.TypeBool,
			Optional:    true,
			Description: "Enable nested hardware virtualization on this virtual machine, facilitating nested virtualization in the guest.",
		},
		"cpu_performance_counters_enabled": {
			Type:        schema.TypeBool,
			Optional:    true,
			Description: "Enable CPU performance counters on this virtual machine.",
		},
		"memory": {
			Type:        schema.TypeInt,
			Optional:    true,
			Default:     1024,
			Description: "The size of the virtual machine's memory, in MB.",
		},
		"memory_reservation_locked_to_max": {
			Type:     schema.TypeBool,
			Optional: true,
			Description: "If set true, memory resource reservation for this virtual machine will always be equal to the virtual machine's memory size;" +
				"increases in memory size will be rejected when a corresponding reservation increase is not possible." +
				" This feature may only be enabled if it is currently possible to reserve all of the virtual machine's memory.",
		},
		"memory_hot_add_enabled": {
			Type:        schema.TypeBool,
			Optional:    true,
			Description: "Allow memory to be added to this virtual machine while it is running.",
		},
		"swap_placement_policy": {
			Type:         schema.TypeString,
			Optional:     true,
			Default:      string(types.VirtualMachineConfigInfoSwapPlacementTypeInherit),
			Description:  "The swap file placement policy for this virtual machine. Can be one of inherit, hostLocal, or vmDirectory.",
			ValidateFunc: validation.StringInSlice(virtualMachineSwapPlacementAllowedValues, false),
		},
		"annotation": {
			Type:        schema.TypeString,
			Optional:    true,
			Computed:    true,
			Description: "User-provided description of the virtual machine.",
		},
		"guest_id": {
			Type:        schema.TypeString,
			Optional:    true,
			Computed:    true,
			Description: "The guest ID for the operating system.",
			DiffSuppressFunc: func(k, oldSetting, newSetting string, d *schema.ResourceData) bool {
				ovf, ok := d.GetOk("ovf_deploy")
				if !ok {
					return false
				}

				if items, ok := ovf.([]interface{}); ok && len(items) > 0 && newSetting == "" {
					return true
				}

				return false
			},
		},
		"alternate_guest_name": {
			Type:        schema.TypeString,
			Optional:    true,
			Description: "The guest name for the operating system when guest_id is otherGuest or otherGuest64.",
		},
		"firmware": {
			Type:         schema.TypeString,
			Optional:     true,
			Default:      string(types.GuestOsDescriptorFirmwareTypeBios),
			Description:  "The firmware interface to use on the virtual machine. Can be one of bios or efi.",
			ValidateFunc: validation.StringInSlice(virtualMachineFirmwareAllowedValues, false),
		},
		"extra_config": {
			Type:        schema.TypeMap,
			Optional:    true,
			Description: "Extra configuration data for this virtual machine. Can be used to supply advanced parameters not normally in configuration, such as instance metadata, or configuration data for OVF images.",
			Elem:        &schema.Schema{Type: schema.TypeString},
		},
		"extra_config_reboot_required": {
			Type:        schema.TypeBool,
			Optional:    true,
			Default:     true,
			Description: "Allow the virtual machine to be rebooted when a change to `extra_config` occurs.",
		},
		"replace_trigger": {
			Type:        schema.TypeString,
			Optional:    true,
			Description: "Triggers replacement of resource whenever it changes.",
			ForceNew:    true,
		},
		"vapp": {
			Type:        schema.TypeList,
			Optional:    true,
			Description: "vApp configuration data for this virtual machine. Can be used to provide configuration data for OVF images.",
			MaxItems:    1,
			Elem:        &schema.Resource{Schema: vAppSubresourceSchema()},
		},
		"vapp_transport": {
			Type:        schema.TypeList,
			Computed:    true,
			Description: "vApp transport methods supported by virtual machine.",
			Elem:        &schema.Schema{Type: schema.TypeString},
		},
		"change_version": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "A unique identifier for a given version of the last configuration applied, such the timestamp of the last update to the configuration.",
		},
		"uuid": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "The UUID of the virtual machine. Also exposed as the ID of the resource.",
		},
		"moid": {
			Type:        schema.TypeString,
			Computed:    true,
			Description: "The machine object ID from VMware vSphere.",
		},
		"storage_policy_id": {
			Type:        schema.TypeString,
			Optional:    true,
			Computed:    true,
			Description: "The ID of the storage policy to assign to the virtual machine home directory.",
		},
		"hardware_version": {
			Type:         schema.TypeInt,
			Optional:     true,
			ValidateFunc: validation.IntBetween(4, 21),
			Description:  "The hardware version for the virtual machine.",
			Computed:     true,
		},
	}
	structure.MergeSchema(s, schemaVirtualMachineResourceAllocation())
	return s
}

// vAppSubresourceSchema represents the schema for the vApp sub-resource.
//
// This sub-resource allows the customization of vApp properties
// on cloned VMs.
func vAppSubresourceSchema() map[string]*schema.Schema {
	return map[string]*schema.Schema{
		"properties": {
			Type:        schema.TypeMap,
			Optional:    true,
			Description: "A map of customizable vApp properties and their values. Allows customization of VMs cloned from OVF templates which have customizable vApp properties.",
			Elem:        &schema.Schema{Type: schema.TypeString},
		},
	}
}

// expandVirtualMachineBootOptions reads certain ResourceData keys and
// returns a VirtualMachineBootOptions.
func expandVirtualMachineBootOptions(d *schema.ResourceData, client *govmomi.Client) *types.VirtualMachineBootOptions {
	obj := &types.VirtualMachineBootOptions{
		BootDelay:        int64(d.Get("boot_delay").(int)),
		BootRetryEnabled: structure.GetBool(d, "boot_retry_enabled"),
		BootRetryDelay:   int64(d.Get("boot_retry_delay").(int)),
	}

	version := viapi.ParseVersionFromClient(client)

	// Minimum Supported Version: 6.5.0
	if version.Newer(viapi.VSphereVersion{Product: version.Product, Major: 6, Minor: 5}) {
		obj.EfiSecureBootEnabled = getBoolWithRestart(d, "efi_secure_boot_enabled")
	}
	return obj
}

// flattenVirtualMachineBootOptions reads various fields from a
// VirtualMachineBootOptions into the passed in ResourceData.
func flattenVirtualMachineBootOptions(d *schema.ResourceData, obj *types.VirtualMachineBootOptions) error {
	_ = d.Set("boot_delay", obj.BootDelay)
	_ = structure.SetBoolPtr(d, "efi_secure_boot_enabled", obj.EfiSecureBootEnabled)
	_ = structure.SetBoolPtr(d, "boot_retry_enabled", obj.BootRetryEnabled)
	_ = d.Set("boot_retry_delay", obj.BootRetryDelay)
	return nil
}

// expandVirtualMachineFlagInfo reads certain ResourceData keys and
// returns a VirtualMachineFlagInfo.
func expandVirtualMachineFlagInfo(d *schema.ResourceData, client *govmomi.Client) *types.VirtualMachineFlagInfo {
	obj := &types.VirtualMachineFlagInfo{
		DiskUuidEnabled:  getBoolWithRestart(d, "enable_disk_uuid"),
		VirtualExecUsage: getWithRestart(d, "hv_mode").(string),
		VirtualMmuUsage:  getWithRestart(d, "ept_rvi_mode").(string),
		EnableLogging:    getBoolWithRestart(d, "enable_logging"),
	}

	version := viapi.ParseVersionFromClient(client)

	// Minimum Supported Version: 6.7.0
	if version.AtLeast(viapi.VSphereVersion{Product: version.Product, Major: 6, Minor: 7}) {
		obj.VbsEnabled = getBoolWithRestart(d, "vbs_enabled")
		obj.VvtdEnabled = getBoolWithRestart(d, "vvtd_enabled")
	}
	return obj
}

// flattenVirtualMachineFlagInfo reads various fields from a
// VirtualMachineFlagInfo into the passed in ResourceData.
func flattenVirtualMachineFlagInfo(d *schema.ResourceData, obj *types.VirtualMachineFlagInfo, client *govmomi.Client) error {
	_ = d.Set("enable_disk_uuid", obj.DiskUuidEnabled)
	_ = d.Set("hv_mode", obj.VirtualExecUsage)
	_ = d.Set("ept_rvi_mode", obj.VirtualMmuUsage)
	_ = d.Set("enable_logging", obj.EnableLogging)

	version := viapi.ParseVersionFromClient(client)

	// Minimum Supported Version: 6.0.0
	if version.AtLeast(viapi.VSphereVersion{Product: version.Product, Major: 6, Minor: 7}) {
		_ = d.Set("vbs_enabled", obj.VbsEnabled)
		_ = d.Set("vvtd_enabled", obj.VvtdEnabled)
	}
	return nil
}

// expandToolsConfigInfo reads certain ResourceData keys and
// returns a ToolsConfigInfo.
func expandToolsConfigInfo(d *schema.ResourceData, client *govmomi.Client) *types.ToolsConfigInfo {
	obj := &types.ToolsConfigInfo{
		SyncTimeWithHost:    structure.GetBool(d, "sync_time_with_host"),
		ToolsUpgradePolicy:  getWithRestart(d, "tools_upgrade_policy").(string),
		AfterPowerOn:        getBoolWithRestart(d, "run_tools_scripts_after_power_on"),
		AfterResume:         getBoolWithRestart(d, "run_tools_scripts_after_resume"),
		BeforeGuestStandby:  getBoolWithRestart(d, "run_tools_scripts_before_guest_standby"),
		BeforeGuestShutdown: getBoolWithRestart(d, "run_tools_scripts_before_guest_shutdown"),
		BeforeGuestReboot:   getBoolWithRestart(d, "run_tools_scripts_before_guest_reboot"),
	}

	version := viapi.ParseVersionFromClient(client)

	// Minimum Supported Version: 7.0.1
	if version.AtLeast(viapi.VSphereVersion{Product: version.Product, Major: 7, Minor: 0, Patch: 1}) {
		obj.SyncTimeWithHostAllowed = structure.GetBool(d, "sync_time_with_host")
		obj.SyncTimeWithHost = structure.GetBool(d, "sync_time_with_host_periodically")
	}
	return obj
}

// flattenToolsConfigInfo reads various fields from a
// ToolsConfigInfo into the passed in ResourceData.
func flattenToolsConfigInfo(d *schema.ResourceData, obj *types.ToolsConfigInfo, client *govmomi.Client) error {
	_ = d.Set("sync_time_with_host", obj.SyncTimeWithHost)
	_ = d.Set("tools_upgrade_policy", obj.ToolsUpgradePolicy)
	_ = d.Set("run_tools_scripts_after_power_on", obj.AfterPowerOn)
	_ = d.Set("run_tools_scripts_after_resume", obj.AfterResume)
	_ = d.Set("run_tools_scripts_before_guest_standby", obj.BeforeGuestStandby)
	_ = d.Set("run_tools_scripts_before_guest_shutdown", obj.BeforeGuestShutdown)
	_ = d.Set("run_tools_scripts_before_guest_reboot", obj.BeforeGuestReboot)

	version := viapi.ParseVersionFromClient(client)

	// Minimum Supported Version: 7.0.1
	if version.AtLeast(viapi.VSphereVersion{Product: version.Product, Major: 7, Minor: 0, Patch: 1}) {
		_ = d.Set("sync_time_with_host", obj.SyncTimeWithHostAllowed)
		_ = d.Set("sync_time_with_host_periodically", obj.SyncTimeWithHost)
	}
	return nil
}

// schemaVirtualMachineResourceAllocation returns the respective schema keys
// for the various kinds of resource allocation settings available to a virtual
// machine. This is an abridged version of ResourceAllocationInfo with only the
// keys present that make sense for virtual machines.
func schemaVirtualMachineResourceAllocation() map[string]*schema.Schema {
	s := make(map[string]*schema.Schema)
	shareLevelFmt := "The allocation level for %s resources. Can be one of high, low, normal, or custom."
	shareCountFmt := "The amount of shares to allocate to %s for a custom share level."
	limitFmt := "The maximum amount of memory (in MB) or CPU (in MHz) that this virtual machine can consume, regardless of available resources."
	reservationFmt := "The amount of memory (in MB) or CPU (in MHz) that this virtual machine is guaranteed."

	for _, t := range virtualMachineResourceAllocationTypeValues {
		shareLevelKey := fmt.Sprintf("%s_share_level", t)
		shareCountKey := fmt.Sprintf("%s_share_count", t)
		limitKey := fmt.Sprintf("%s_limit", t)
		reservationKey := fmt.Sprintf("%s_reservation", t)

		s[shareLevelKey] = &schema.Schema{
			Type:         schema.TypeString,
			Optional:     true,
			Default:      string(types.SharesLevelNormal),
			Description:  fmt.Sprintf(shareLevelFmt, t),
			ValidateFunc: validation.StringInSlice(sharesLevelAllowedValues, false),
		}
		s[shareCountKey] = &schema.Schema{
			Type:         schema.TypeInt,
			Optional:     true,
			Computed:     true,
			Description:  fmt.Sprintf(shareCountFmt, t),
			ValidateFunc: validation.IntAtLeast(0),
		}
		s[limitKey] = &schema.Schema{
			Type:         schema.TypeInt,
			Optional:     true,
			Default:      -1,
			Description:  limitFmt,
			ValidateFunc: validation.IntAtLeast(-1),
		}
		s[reservationKey] = &schema.Schema{
			Type:         schema.TypeInt,
			Optional:     true,
			Description:  reservationFmt,
			ValidateFunc: validation.IntAtLeast(0),
		}
	}

	return s
}

// expandVirtualMachineResourceAllocation reads the VM resource allocation
// resource data keys for the type supplied by key and returns an appropriate
// types.ResourceAllocationInfo reference.
func expandVirtualMachineResourceAllocation(d *schema.ResourceData, key string) *types.ResourceAllocationInfo {
	shareLevelKey := fmt.Sprintf("%s_share_level", key)
	shareCountKey := fmt.Sprintf("%s_share_count", key)
	limitKey := fmt.Sprintf("%s_limit", key)
	reservationKey := fmt.Sprintf("%s_reservation", key)

	obj := &types.ResourceAllocationInfo{
		Limit:       structure.GetInt64PtrEmptyZero(d, limitKey),
		Reservation: structure.GetInt64PtrEmptyZero(d, reservationKey),
	}
	shares := &types.SharesInfo{
		Level:  types.SharesLevel(d.Get(shareLevelKey).(string)),
		Shares: int32(d.Get(shareCountKey).(int)),
	}
	obj.Shares = shares
	return obj
}

// expandLatencySensitivity reads certain ResourceData keys and returns a
// LatencySensitivity.
func expandLatencySensitivity(d *schema.ResourceData) *types.LatencySensitivity {
	obj := &types.LatencySensitivity{
		Level: types.LatencySensitivitySensitivityLevel(d.Get("latency_sensitivity").(string)),
	}
	return obj
}

// flattenLatencySensitivity reads various fields from a LatencySensitivity and
// sets appropriate keys in the supplied ResourceData.
func flattenLatencySensitivity(d *schema.ResourceData, obj *types.LatencySensitivity) error {
	if obj == nil {
		log.Printf("[WARN] Unable to read LatencySensitivity, skipping")
		return nil
	}
	return d.Set("latency_sensitivity", obj.Level)
}

// flattenVirtualMachineResourceAllocation reads various fields from a
// ResourceAllocationInfo and sets appropriate keys in the
// supplied ResourceData.
func flattenVirtualMachineResourceAllocation(d *schema.ResourceData, obj *types.ResourceAllocationInfo, key string) error {
	shareLevelKey := fmt.Sprintf("%s_share_level", key)
	shareCountKey := fmt.Sprintf("%s_share_count", key)
	limitKey := fmt.Sprintf("%s_limit", key)
	reservationKey := fmt.Sprintf("%s_reservation", key)

	_ = structure.SetInt64Ptr(d, limitKey, obj.Limit)
	_ = structure.SetInt64Ptr(d, reservationKey, obj.Reservation)
	if obj.Shares != nil {
		_ = d.Set(shareLevelKey, obj.Shares.Level)
		_ = d.Set(shareCountKey, obj.Shares.Shares)
	}
	return nil
}

// expandExtraConfig reads in all the extra_config key/value pairs and returns
// the appropriate OptionValue slice.
//
// We track changes to keys to determine if any have been removed from
// configuration - if they have, we add them with a nil value to ensure they
// are removed from extraConfig on the update.
func expandExtraConfig(d *schema.ResourceData) []types.BaseOptionValue {
	if d.HasChange("extra_config") {
		// While there's a possibility that modification of some settings in
		// extraConfig may not require a restart, there's no real way for us to
		// know, hence we just default to requiring a reboot here.
		rebootRequired := true
		// Check for an override to the default reboot when changes are made to the extraConfig.
		_rebootRequired, ok := d.Get("extra_config_reboot_required").(bool)
		if ok {
			rebootRequired = _rebootRequired
		}
		_ = d.Set("reboot_required", rebootRequired)
	} else {
		// There's no change here, so we might as well just return a nil set, which
		// is a no-op for modification of extraConfig.
		return nil
	}
	var opts []types.BaseOptionValue

	// Nil out removed values
	old, newValue := d.GetChange("extra_config")
	for k1 := range old.(map[string]interface{}) {
		var found bool
		for k2 := range newValue.(map[string]interface{}) {
			if k1 == k2 {
				found = true
			}
		}
		if !found {
			ov := &types.OptionValue{
				Key:   k1,
				Value: "",
			}
			opts = append(opts, ov)
		}
	}

	// Look for new values, in addition to changed values.
	for k1, v1 := range newValue.(map[string]interface{}) {
		var found bool
		for k2, v2 := range old.(map[string]interface{}) {
			if k1 == k2 {
				found = true
				if v1 != v2 {
					// Value has changed, add it to the changeset
					ov := &types.OptionValue{
						Key:   k1,
						Value: types.AnyType(v1),
					}
					opts = append(opts, ov)
				}
			}
		}
		if !found {
			// Brand new value
			ov := &types.OptionValue{
				Key:   k1,
				Value: types.AnyType(v1),
			}
			opts = append(opts, ov)
		}
	}

	// Done!
	return opts
}

// flattenExtraConfig reads in the extraConfig from a running virtual machine
// and *only* sets the keys in extra_config that we know about. This is to
// prevent Terraform from interfering with values that are maintained
// out-of-band by vSphere which could lead to spurious diffs and unstable
// operation.  Note the side-effect here is that Terraform cannot track manual
// drift that is not a part of normal vSphere operation. Removing keys that
// have been in configuration through at least one successful apply though are
// safe, as removing them will add a nil value for that key in the next
// chnageset, properly effecting its removal.
func flattenExtraConfig(d *schema.ResourceData, opts []types.BaseOptionValue) error {
	if len(opts) < 1 {
		// No opts to read is a no-op
		return nil
	}
	ec := make(map[string]interface{})
	for _, v := range opts {
		ov := v.GetOptionValue()
		for k := range d.Get("extra_config").(map[string]interface{}) {
			if ov.Key == k {
				ec[ov.Key] = ov.Value
			}
		}
	}
	return d.Set("extra_config", ec)
}

// expandVAppConfig reads in all the vapp key/value pairs and returns
// the appropriate VmConfigSpec.
//
// We track changes to keys to determine if any have been removed from
// configuration - if they have, we add them with an empty value to ensure
// they are removed from vAppConfig on the update.
func expandVAppConfig(d *schema.ResourceData, client *govmomi.Client) (*types.VmConfigSpec, error) {
	if !d.HasChange("vapp") {
		return nil, nil
	}

	// Many vApp config values, such as IP address, will require a
	// restart of the machine to properly apply. We don't necessarily
	// know which ones they are, so we will restart for every change.
	_ = d.Set("reboot_required", true)

	var props []types.VAppPropertySpec

	_, newValue := d.GetChange("vapp")
	newMap := make(map[string]interface{})

	newVApps := newValue.([]interface{})
	if len(newVApps) > 0 && newVApps[0] != nil {
		newVApp := newVApps[0].(map[string]interface{})
		if props, ok := newVApp["properties"].(map[string]interface{}); ok {
			propsCopy, err := copystructure.Copy(props)
			if err != nil {
				return nil, fmt.Errorf("while extracting vapp properties into a new map: %s", err)
			}
			newMap = propsCopy.(map[string]interface{})
		}
	}

	uuid := d.Id()
	if uuid == "" {
		// No virtual machine has been created, this usually means that this is a
		// brand new virtual machine. vApp properties are not supported on this
		// workflow, so if there are any defined, return an error indicating such.
		// Return with a no-op otherwise.
		if len(newMap) > 0 {
			return nil, fmt.Errorf("vApp properties can only be set on cloned virtual machines")
		}
		return nil, nil
	}
	vm, _ := virtualmachine.FromUUID(client, d.Id())
	vmProps, _ := virtualmachine.Properties(vm)
	if vmProps.Config.VAppConfig == nil {
		return nil, fmt.Errorf("this VM lacks a vApp configuration and cannot have vApp properties set on it")
	}
	allProperties := vmProps.Config.VAppConfig.GetVmConfigInfo().Property

	enableHiddenProperties := d.Get("ovf_deploy.0.enable_hidden_properties").(bool)

	for _, p := range allProperties {
		if enableHiddenProperties {
			defaultValue := " "
			if p.DefaultValue != "" {
				defaultValue = p.DefaultValue
			}
			userConfigurable := true
			prop := types.VAppPropertySpec{
				ArrayUpdateSpec: types.ArrayUpdateSpec{
					Operation: types.ArrayUpdateOperationEdit,
				},
				Info: &types.VAppPropertyInfo{
					Key:              p.Key,
					Id:               p.Id,
					Value:            defaultValue,
					UserConfigurable: &userConfigurable,
				},
			}

			newValue, ok := newMap[p.Id]
			if ok {
				prop.Info.Value = newValue.(string)
				delete(newMap, p.Id)
			}
			props = append(props, prop)
		} else {
			if *p.UserConfigurable {
				defaultValue := " "
				if p.DefaultValue != "" {
					defaultValue = p.DefaultValue
				}
				prop := types.VAppPropertySpec{
					ArrayUpdateSpec: types.ArrayUpdateSpec{
						Operation: types.ArrayUpdateOperationEdit,
					},
					Info: &types.VAppPropertyInfo{
						Key:              p.Key,
						Id:               p.Id,
						Value:            defaultValue,
						UserConfigurable: p.UserConfigurable,
					},
				}

				newValue, ok := newMap[p.Id]
				if ok {
					prop.Info.Value = newValue.(string)
					delete(newMap, p.Id)
				}
				props = append(props, prop)
			} else {
				_, ok := newMap[p.Id]
				if ok {
					return nil, fmt.Errorf("vApp property with userConfigurable=false specified in vapp.properties: %+v", reflect.ValueOf(newMap).MapKeys())
				}
			}
		}
	}

	if len(newMap) > 0 {
		return nil, fmt.Errorf("unsupported vApp properties in vapp.properties: %+v", reflect.ValueOf(newMap).MapKeys())
	}

	return &types.VmConfigSpec{
		Property: props,
	}, nil
}

// flattenVAppConfig reads in the vAppConfig from a running virtual machine
// and sets all keys in vapp.
func flattenVAppConfig(d *schema.ResourceData, config types.BaseVmConfigInfo) error {
	if config == nil {
		_ = d.Set("vapp_transport", []string{})
		return nil
	}
	// Set `vapp_config here while config is available to avoid extra API calls
	_ = d.Set("vapp_transport", config.GetVmConfigInfo().OvfEnvironmentTransport)

	props := config.GetVmConfigInfo().Property
	if len(props) < 1 {
		// No props to read is a no-op
		return nil
	}
	vac := make(map[string]interface{})
	for _, v := range props {
		if *v.UserConfigurable {
			if v.Value != "" && v.Value != v.DefaultValue {
				vac[v.Id] = v.Value
			}
		}
	}
	// Only set if properties exist to prevent creating an unnecessary diff
	if len(vac) > 0 {
		return d.Set("vapp", []interface{}{
			map[string]interface{}{
				"properties": vac,
			},
		})
	}
	return nil
}

// expandCPUCountConfig is a helper for expandVirtualMachineConfigSpec that
// determines if we need to restart the VM due to a change in CPU count. This
// is determined by the net change in CPU count and the pre-update values of
// cpu_hot_add_enabled and cpu_hot_remove_enabled. The pre-update value is
// important here as while CPU hot-add/remove is supported while the values are
// enabled on the virtual machine, modification of hot-add/remove themselves is
// an operation that requires a power down of the VM.
func expandCPUCountConfig(d *schema.ResourceData) int32 {
	occ, ncc := d.GetChange("num_cpus")
	cha, _ := d.GetChange("cpu_hot_add_enabled")
	currentHotAdd := cha.(bool)
	chr, _ := d.GetChange("cpu_hot_remove_enabled")
	currentHotRemove := chr.(bool)
	oldCPUCount := int32(occ.(int))
	newCPUCount := int32(ncc.(int))

	switch {
	case oldCPUCount < newCPUCount:
		// Adding CPUs
		if !currentHotAdd {
			log.Printf("[DEBUG] %s: CPU operation requires a VM restart", resourceVSphereVirtualMachineIDString(d))
			_ = d.Set("reboot_required", true)
		}
	case oldCPUCount > newCPUCount:
		// Removing CPUs
		if !currentHotRemove {
			log.Printf("[DEBUG] %s: CPU operation requires a VM restart", resourceVSphereVirtualMachineIDString(d))
			_ = d.Set("reboot_required", true)
		}
	}
	return newCPUCount
}

// expandMemorySizeConfig is a helper for expandVirtualMachineConfigSpec that
// determines if we need to restart the system to increase the amount of
// available memory on the system. This is determined by the current (or in
// other words, the old, pre-update setting) of memory_hot_add_enabled.
func expandMemorySizeConfig(d *schema.ResourceData) int64 {
	om, nm := d.GetChange("memory")
	cha, _ := d.GetChange("memory_hot_add_enabled")
	currentHotAdd := cha.(bool)
	oldMem := int64(om.(int))
	newMem := int64(nm.(int))

	switch {
	case oldMem < newMem:
		// Adding CPUs
		if !currentHotAdd {
			log.Printf("[DEBUG] %s: Memory operation requires a VM restart", resourceVSphereVirtualMachineIDString(d))
			_ = d.Set("reboot_required", true)
		}
	case oldMem > newMem:
		// Removing memory always requires a reboot
		log.Printf("[DEBUG] %s: Memory operation requires a VM restart", resourceVSphereVirtualMachineIDString(d))
		_ = d.Set("reboot_required", true)
	}
	return newMem
}

// expandVirtualMachineProfileSpec reads storage policy ID from ResourceData and
// returns VirtualMachineProfileSpec.
func expandVirtualMachineProfileSpec(d *schema.ResourceData) []types.BaseVirtualMachineProfileSpec {
	if policyID := d.Get("storage_policy_id").(string); policyID != "" {
		return spbm.PolicySpecByID(policyID)
	}

	return nil
}

// expandVirtualMachineConfigSpec reads certain ResourceData keys and
// returns a VirtualMachineConfigSpec.
func expandVirtualMachineConfigSpec(d *schema.ResourceData, client *govmomi.Client) (types.VirtualMachineConfigSpec, error) {
	log.Printf("[DEBUG] %s: Building config spec", resourceVSphereVirtualMachineIDString(d))
	vappConfig, err := expandVAppConfig(d, client)
	if err != nil {
		return types.VirtualMachineConfigSpec{}, err
	}

	obj := types.VirtualMachineConfigSpec{
		Name:                         d.Get("name").(string),
		GuestId:                      getWithRestart(d, "guest_id").(string),
		AlternateGuestName:           getWithRestart(d, "alternate_guest_name").(string),
		Annotation:                   d.Get("annotation").(string),
		Tools:                        expandToolsConfigInfo(d, client),
		Flags:                        expandVirtualMachineFlagInfo(d, client),
		NumCPUs:                      expandCPUCountConfig(d),
		NumCoresPerSocket:            int32(getWithRestart(d, "num_cores_per_socket").(int)),
		MemoryMB:                     expandMemorySizeConfig(d),
		MemoryHotAddEnabled:          getBoolWithRestart(d, "memory_hot_add_enabled"),
		CpuHotAddEnabled:             getBoolWithRestart(d, "cpu_hot_add_enabled"),
		CpuHotRemoveEnabled:          getBoolWithRestart(d, "cpu_hot_remove_enabled"),
		CpuAllocation:                expandVirtualMachineResourceAllocation(d, "cpu"),
		MemoryAllocation:             expandVirtualMachineResourceAllocation(d, "memory"),
		MemoryReservationLockedToMax: getMemoryReservationLockedToMax(d),
		ExtraConfig:                  expandExtraConfig(d),
		SwapPlacement:                getWithRestart(d, "swap_placement_policy").(string),
		BootOptions:                  expandVirtualMachineBootOptions(d, client),
		VAppConfig:                   vappConfig,
		Firmware:                     getWithRestart(d, "firmware").(string),
		NestedHVEnabled:              getBoolWithRestart(d, "nested_hv_enabled"),
		VPMCEnabled:                  getBoolWithRestart(d, "cpu_performance_counters_enabled"),
		LatencySensitivity:           expandLatencySensitivity(d),
		VmProfile:                    expandVirtualMachineProfileSpec(d),
		Version:                      virtualmachine.GetHardwareVersionID(d.Get("hardware_version").(int)),
	}

	return obj, nil
}

// flattenVirtualMachineConfigInfo reads various fields from a
// VirtualMachineConfigInfo into the passed in ResourceData.
//
// This is the flatten counterpart to expandVirtualMachineConfigSpec.
func flattenVirtualMachineConfigInfo(d *schema.ResourceData, obj *types.VirtualMachineConfigInfo, client *govmomi.Client) error {
	_ = d.Set("name", obj.Name)
	_ = d.Set("guest_id", obj.GuestId)
	_ = d.Set("alternate_guest_name", obj.AlternateGuestName)
	_ = d.Set("annotation", obj.Annotation)
	_ = d.Set("num_cpus", obj.Hardware.NumCPU)
	_ = d.Set("num_cores_per_socket", obj.Hardware.NumCoresPerSocket)
	_ = d.Set("memory", obj.Hardware.MemoryMB)
	_ = d.Set("memory_hot_add_enabled", obj.MemoryHotAddEnabled)

	memoryReservationLockedToMax := false
	if obj.MemoryReservationLockedToMax != nil {
		memoryReservationLockedToMax = *obj.MemoryReservationLockedToMax
	}
	_ = d.Set("memory_reservation_locked_to_max", memoryReservationLockedToMax)
	_ = d.Set("cpu_hot_add_enabled", obj.CpuHotAddEnabled)
	_ = d.Set("cpu_hot_remove_enabled", obj.CpuHotRemoveEnabled)
	_ = d.Set("swap_placement_policy", obj.SwapPlacement)
	_ = d.Set("firmware", obj.Firmware)
	_ = d.Set("nested_hv_enabled", obj.NestedHVEnabled)
	_ = d.Set("cpu_performance_counters_enabled", obj.VPMCEnabled)
	_ = d.Set("change_version", obj.ChangeVersion)
	_ = d.Set("uuid", obj.Uuid)
	_ = d.Set("hardware_version", virtualmachine.GetHardwareVersionNumber(obj.Version))

	if err := flattenToolsConfigInfo(d, obj.Tools, client); err != nil {
		return err
	}
	if err := flattenVirtualMachineFlagInfo(d, &obj.Flags, client); err != nil {
		return err
	}
	if err := flattenVirtualMachineResourceAllocation(d, obj.CpuAllocation, "cpu"); err != nil {
		return err
	}
	if err := flattenVirtualMachineResourceAllocation(d, obj.MemoryAllocation, "memory"); err != nil {
		return err
	}
	if err := flattenExtraConfig(d, obj.ExtraConfig); err != nil {
		return err
	}
	if err := flattenVAppConfig(d, obj.VAppConfig); err != nil {
		return err
	}
	if err := flattenLatencySensitivity(d, obj.LatencySensitivity); err != nil {
		return err
	}

	// This method does not operate any different than the above method but we
	// return its error result directly to ensure there are no warnings in the
	// linter. It's awkward, but golint does not allow setting exceptions.
	return flattenVirtualMachineBootOptions(d, obj.BootOptions)
}

// expandVirtualMachineConfigSpecChanged compares an existing
// VirtualMachineConfigInfo with a VirtualMachineConfigSpec generated from
// existing resource data and compares them to see if there is a change. The new spec
//
// It does this be creating a fake ResourceData off of the VM resource schema,
// flattening the config info into that, and then expanding both ResourceData
// instances and comparing the resultant ConfigSpecs.
func expandVirtualMachineConfigSpecChanged(d *schema.ResourceData, client *govmomi.Client, info *types.VirtualMachineConfigInfo) (types.VirtualMachineConfigSpec, bool, error) {
	// Create the fake ResourceData from the VM resource
	oldData := resourceVSphereVirtualMachine().Data(&terraform.InstanceState{})
	oldData.SetId(d.Id())
	// Flatten the old config info into it
	err := flattenVirtualMachineConfigInfo(oldData, info, client)
	if err != nil {
		return types.VirtualMachineConfigSpec{}, false, err
	}
	// Read state back in. This is necessary to ensure GetChange calls work
	// correctly.
	oldData = resourceVSphereVirtualMachine().Data(oldData.State())
	// Get both specs.
	log.Printf("[DEBUG] %s: Expanding old config. Ignore reboot_required messages", resourceVSphereVirtualMachineIDString(d))
	oldSpec, err := expandVirtualMachineConfigSpec(oldData, client)
	if err != nil {
		return types.VirtualMachineConfigSpec{}, false, err
	}
	log.Printf("[DEBUG] %s: Expanding of old config complete", resourceVSphereVirtualMachineIDString(d))

	newSpec, err := expandVirtualMachineConfigSpec(d, client)
	if err != nil {
		return types.VirtualMachineConfigSpec{}, false, err
	}

	isVMConfigSpecChanged := !reflect.DeepEqual(oldSpec, newSpec)
	// Don't include the hardware version in the UpdateSpec. It is only needed
	// when creating new VMs.
	newSpec.Version = ""

	// Return the new spec and compare
	return newSpec, isVMConfigSpecChanged, nil
}

// getMemoryReservationLockedToMax determines if the memory_reservation is not
// set to be equal to memory. If they are not equal, then the memory
// reservation needs to be unlocked from the maximum. Rather than supporting
// the locking reservation to max option, we can set memory_reservation to
// memory in the configuration. Not supporting the option causes problems when
// cloning from a template that has it enabled. The solution is to set it to
// false when needed, but leave it alone when the change is not necessary.
func getMemoryReservationLockedToMax(d *schema.ResourceData) *bool {
	memory := d.Get("memory").(int)
	memoryReservation := d.Get("memory_reservation").(int)
	memoryLockMax := d.Get("memory_reservation_locked_to_max").(bool)

	if memory != memoryReservation {
		return structure.BoolPtr(false)
	}

	if memory == memoryReservation && memoryLockMax {
		return structure.BoolPtr(true)
	}

	return nil
}
