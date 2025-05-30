// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.
// SPDX-License-Identifier: MPL-2.0

package vsphere

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
	"github.com/vmware/terraform-provider-vsphere/vsphere/internal/helper/hostsystem"
	"github.com/vmware/terraform-provider-vsphere/vsphere/internal/helper/structure"
)

const (
	vnicServiceTypeVsan       = "vsan"
	vnicServiceTypeVmotion    = "vmotion"
	vnicServiceTypeManagement = "management"
)

var vnicServiceTypeAllowedValues = []string{
	vnicServiceTypeVsan,
	vnicServiceTypeVmotion,
	vnicServiceTypeManagement,
}

func resourceVsphereNic() *schema.Resource {
	return &schema.Resource{
		Create: resourceVsphereNicCreate,
		Read:   resourceVsphereNicRead,
		Update: resourceVsphereNicUpdate,
		Delete: resourceVsphereNicDelete,
		Importer: &schema.ResourceImporter{
			State: resourceVSphereNicImport,
		},
		Schema: vNicSchema(),
	}
}

func vNicSchema() map[string]*schema.Schema {
	base := BaseVMKernelSchema()
	base["host"] = &schema.Schema{
		Type:        schema.TypeString,
		Required:    true,
		Description: "ESX host the interface belongs to",
		ForceNew:    true,
	}

	return base
}

func resourceVsphereNicRead(d *schema.ResourceData, meta interface{}) error {
	log.Printf("[DEBUG] starting resource_vnic")
	ctx := context.TODO()
	client := meta.(*Client).vimClient

	hostID, nicID := splitHostIDNicID(d)

	vnic, err := getVnicFromHost(ctx, client, hostID, nicID)
	if err != nil {
		log.Printf("[DEBUG] Nic (%s) not found. Probably deleted.", nicID)
		d.SetId("")
		return nil
	}

	_ = d.Set("netstack", vnic.Spec.NetStackInstanceKey)
	_ = d.Set("portgroup", vnic.Portgroup)
	if vnic.Spec.DistributedVirtualPort != nil {
		_ = d.Set("distributed_switch_port", vnic.Spec.DistributedVirtualPort.SwitchUuid)
		_ = d.Set("distributed_port_group", vnic.Spec.DistributedVirtualPort.PortgroupKey)
	}
	_ = d.Set("mtu", vnic.Spec.Mtu)
	_ = d.Set("mac", vnic.Spec.Mac)

	// Do we have any ipv4 config ?
	// IpAddress will be an empty string if ipv4 is off
	if vnic.Spec.Ip.IpAddress != "" {
		// if DHCP is true then we should ignore whatever addresses are set here.
		ipv4dict := make(map[string]interface{})
		ipv4dict["dhcp"] = vnic.Spec.Ip.Dhcp
		if !vnic.Spec.Ip.Dhcp {
			ipv4dict["ip"] = vnic.Spec.Ip.IpAddress
			ipv4dict["netmask"] = vnic.Spec.Ip.SubnetMask
			if vnic.Spec.IpRouteSpec != nil {
				ipv4dict["gw"] = vnic.Spec.IpRouteSpec.IpRouteConfig.GetHostIpRouteConfig().DefaultGateway
			}
		}
		err = d.Set("ipv4", []map[string]interface{}{ipv4dict})
		if err != nil {
			return err
		}
	}

	// Do we have any ipv6 config ?
	// IpV6Config will be nil if ipv6 is off
	if vnic.Spec.Ip.IpV6Config != nil {
		ipv6dict := map[string]interface{}{
			"dhcp":       *vnic.Spec.Ip.IpV6Config.DhcpV6Enabled,
			"autoconfig": *vnic.Spec.Ip.IpV6Config.AutoConfigurationEnabled,
		}

		// First we need to filter out addresses that were configured via dhcp or autoconfig
		// or link local or any other mechanism
		addrList := make([]string, 0)
		for _, addr := range vnic.Spec.Ip.IpV6Config.IpV6Address {
			if addr.Origin == "manual" {
				addrList = append(addrList, fmt.Sprintf("%s/%d", addr.IpAddress, addr.PrefixLength))
			}
		}
		if (len(addrList) == 0) && !*vnic.Spec.Ip.IpV6Config.DhcpV6Enabled && !*vnic.Spec.Ip.IpV6Config.AutoConfigurationEnabled {
			_ = d.Set("ipv6", nil)
		} else {
			ipv6dict["addresses"] = addrList

			if vnic.Spec.IpRouteSpec != nil {
				ipv6dict["gw"] = vnic.Spec.IpRouteSpec.IpRouteConfig.GetHostIpRouteConfig().IpV6DefaultGateway
			} else if _, ok := d.GetOk("ipv6.0.gw"); ok {
				// There is a gw set in the config, but none set on the Host.
				ipv6dict["gw"] = ""
			}
			err = d.Set("ipv6", []map[string]interface{}{ipv6dict})
			if err != nil {
				return err
			}
		}
	}

	// get enabled services
	hostSystem, err := hostsystem.FromID(client, hostID)
	if err != nil {
		return err
	}

	hostVnicMgr, err := hostSystem.ConfigManager().VirtualNicManager(ctx)
	if err != nil {
		return nil
	}

	hostVnicMgrInfo, err := hostVnicMgr.Info(ctx)
	if err != nil {
		return nil
	}

	var services []string
	for _, netConfig := range hostVnicMgrInfo.NetConfig {
		for _, vnic := range netConfig.SelectedVnic {
			if isNicIDContained := strings.Contains(vnic, nicID); isNicIDContained {
				services = append(services, netConfig.NicType)
			}
		}
	}
	if err := d.Set("services", schema.NewSet(schema.HashString, structure.SliceStringsToInterfaces(services))); err != nil {
		return err
	}

	return nil
}

func resourceVsphereNicCreate(d *schema.ResourceData, meta interface{}) error {
	nicID, err := createVNic(d, meta)
	if err != nil {
		return err
	}
	log.Printf("[DEBUG] Created NIC with ID: %s", nicID)
	hostID := d.Get("host")
	tfNicID := fmt.Sprintf("%s_%s", hostID, nicID)
	d.SetId(tfNicID)
	return resourceVsphereNicRead(d, meta)
}

func resourceVsphereNicUpdate(d *schema.ResourceData, meta interface{}) error {
	for _, k := range []string{
		"portgroup", "distributed_switch_port", "distributed_port_group",
		"mac", "mtu", "ipv4", "ipv6", "netstack", "services"} {
		if d.HasChange(k) {
			_, err := updateVNic(d, meta)
			if err != nil {
				return err
			}
			break
		}
	}
	return resourceVsphereNicRead(d, meta)
}

func resourceVsphereNicDelete(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*Client).vimClient
	hostID, nicID := splitHostIDNicID(d)

	err := removeVnic(client, hostID, nicID)
	if err != nil {
		return err
	}
	return resourceVsphereNicRead(d, meta)
}

func resourceVSphereNicImport(d *schema.ResourceData, _ interface{}) ([]*schema.ResourceData, error) {
	hostID, _ := splitHostIDNicID(d)

	err := d.Set("host", hostID)
	if err != nil {
		return []*schema.ResourceData{}, err
	}

	return []*schema.ResourceData{d}, nil
}

// BaseVMKernelSchema returns the schema required to represent a vNIC adapter on an ESX Host.
// We make this public so we can pull this from the host resource as well.
func BaseVMKernelSchema() map[string]*schema.Schema {
	sch := map[string]*schema.Schema{
		"portgroup": {
			Type:        schema.TypeString,
			Optional:    true,
			Description: "portgroup to attach the nic to. Do not set if you set distributed_switch_port.",
		},
		"distributed_switch_port": {
			Type:        schema.TypeString,
			Optional:    true,
			Description: "UUID of the DVSwitch the nic will be attached to. Do not set if you set portgroup.",
		},
		"distributed_port_group": {
			Type:        schema.TypeString,
			Optional:    true,
			Description: "Key of the distributed portgroup the nic will connect to",
		},
		"ipv4": {
			Type:     schema.TypeList,
			Optional: true,
			MaxItems: 1,
			Elem: &schema.Resource{Schema: map[string]*schema.Schema{
				"dhcp": {
					Type:        schema.TypeBool,
					Optional:    true,
					Description: "Use DHCP to configure the interface's IPv4 stack.",
				},
				"ip": {
					Type:        schema.TypeString,
					Optional:    true,
					Description: "address of the interface, if DHCP is not set.",
				},
				"netmask": {
					Type:        schema.TypeString,
					Optional:    true,
					Description: "netmask of the interface, if DHCP is not set.",
				},
				"gw": {
					Type:        schema.TypeString,
					Optional:    true,
					Description: "IP address of the default gateway, if DHCP is not set.",
				},
			}},
		},
		"ipv6": {
			Type:     schema.TypeList,
			Optional: true,
			MaxItems: 1,
			Elem: &schema.Resource{Schema: map[string]*schema.Schema{
				"dhcp": {
					Type:        schema.TypeBool,
					Optional:    true,
					Description: "Use DHCP to configure the interface's IPv4 stack.",
				},
				"autoconfig": {
					Type:        schema.TypeBool,
					Optional:    true,
					Description: "Use IPv6 Autoconfiguration (RFC2462).",
				},
				"addresses": {
					Type:        schema.TypeList,
					Optional:    true,
					Description: "List of IPv6 addresses",
					Elem: &schema.Schema{
						Type: schema.TypeString,
					},
					DiffSuppressFunc: func(k, old, newValue string, d *schema.ResourceData) bool {
						return strings.EqualFold(old, newValue)
					},
				},
				"gw": {
					Type:        schema.TypeString,
					Optional:    true,
					Description: "IP address of the default gateway, if DHCP or autoconfig is not set.",
					DiffSuppressFunc: func(k, old, newValue string, d *schema.ResourceData) bool {
						return strings.EqualFold(old, newValue)
					},
				},
			}},
		},
		"mac": {
			Type:        schema.TypeString,
			Optional:    true,
			Computed:    true,
			Description: "MAC address of the interface.",
		},
		"mtu": {
			Type:        schema.TypeInt,
			Optional:    true,
			Computed:    true,
			Description: "MTU of the interface.",
		},
		"netstack": {
			Type:        schema.TypeString,
			Optional:    true,
			Description: "TCP/IP stack setting for this interface. Possible values are 'defaultTcpipStack', 'vmotion', 'provisioning'",
			Default:     "defaultTcpipStack",
			ForceNew:    true,
		},
		"services": {
			Type:        schema.TypeSet,
			Optional:    true,
			Description: "Enabled services setting for this interface. Current possible values are 'vmotion', 'management' and 'vsan'",
			Elem: &schema.Schema{
				Type:         schema.TypeString,
				ValidateFunc: validation.StringInSlice(vnicServiceTypeAllowedValues, false),
			},
		},
	}
	return sch
}

func updateVNic(d *schema.ResourceData, meta interface{}) (string, error) {
	err := precheckEnableServices(d)
	if err != nil {
		return "", err
	}

	client := meta.(*Client).vimClient
	hostID, nicID := splitHostIDNicID(d)
	ctx := context.TODO()

	nic, err := getNicSpecFromSchema(d)
	if err != nil {
		return "", err
	}

	hns, err := getHostNetworkSystem(client, hostID)
	if err != nil {
		return "", err
	}

	err = hns.UpdateVirtualNic(ctx, nicID, *nic)
	if err != nil {
		return "", err
	}

	err = updateVnicService(d, hostID, nicID, meta)
	if err != nil {
		return "", err
	}

	return nicID, nil
}

func updateVnicService(d *schema.ResourceData, hostID string, nicID string, meta interface{}) error {
	serviceOld, serviceNew := d.GetChange("services")
	deleteList := serviceOld.(*schema.Set).List()
	addList := serviceNew.(*schema.Set).List()

	client := meta.(*Client).vimClient
	ctx := context.TODO()
	hostSystem, err := hostsystem.FromID(client, hostID)
	if err != nil {
		return err
	}
	method, err := hostSystem.ConfigManager().VirtualNicManager(ctx)
	if err != nil {
		return nil
	}

	for _, value := range deleteList {
		err = method.DeselectVnic(ctx, value.(string), nicID)
		if err != nil {
			return err
		}
	}

	for _, value := range addList {
		err = method.SelectVnic(ctx, value.(string), nicID)
		if err != nil {
			return err
		}
	}

	return nil
}

func precheckEnableServices(d *schema.ResourceData) error {
	if d.Get("netstack").(string) != "defaultTcpipStack" && len(d.Get("services").(*schema.Set).List()) != 0 {
		return fmt.Errorf("services can only be configured when netstack is set to defaultTcpipStack")
	}
	return nil
}

func createVNic(d *schema.ResourceData, meta interface{}) (string, error) {
	err := precheckEnableServices(d)
	if err != nil {
		return "", err
	}

	client := meta.(*Client).vimClient
	ctx := context.TODO()

	nic, err := getNicSpecFromSchema(d)
	if err != nil {
		return "", err
	}

	hostID := d.Get("host").(string)
	hns, err := getHostNetworkSystem(client, hostID)
	if err != nil {
		return "", err
	}

	portgroup := d.Get("portgroup").(string)
	nicID, err := hns.AddVirtualNic(ctx, portgroup, *nic)
	if err != nil {
		return "", err
	}
	d.SetId(fmt.Sprintf("%s_%s", hostID, nicID))

	err = updateVnicService(d, hostID, nicID, meta)
	if err != nil {
		return "", err
	}

	return nicID, nil
}

func removeVnic(client *govmomi.Client, hostID, nicID string) error {
	hns, err := getHostNetworkSystem(client, hostID)
	if err != nil {
		return err
	}

	return hns.RemoveVirtualNic(context.TODO(), nicID)
}

func getHostNetworkSystem(client *govmomi.Client, hostID string) (*object.HostNetworkSystem, error) {
	ctx := context.TODO()

	host, err := hostsystem.FromID(client, hostID)
	if err != nil {
		return nil, err
	}
	cmRef := host.ConfigManager().Reference()
	cm := object.NewHostConfigManager(client.Client, cmRef)
	hns, err := cm.NetworkSystem(ctx)
	if err != nil {
		log.Printf("[DEBUG] Failed to access the host's NetworkSystem service: %s", err)
		return nil, err
	}
	return hns, nil
}

func getNicSpecFromSchema(d *schema.ResourceData) (*types.HostVirtualNicSpec, error) {
	portgroup := d.Get("portgroup").(string)
	dvp := d.Get("distributed_switch_port").(string)
	dpg := d.Get("distributed_port_group").(string)
	mac := d.Get("mac").(string)
	mtu := int32(d.Get("mtu").(int))

	if portgroup != "" && dvp != "" {
		return nil, fmt.Errorf("portgroup and distributed_switch_port settings are mutually exclusive")
	}

	var dvpPortConnection *types.DistributedVirtualSwitchPortConnection
	if portgroup != "" {
		dvpPortConnection = nil
	} else {
		dvpPortConnection = &types.DistributedVirtualSwitchPortConnection{
			SwitchUuid:   dvp,
			PortgroupKey: dpg,
		}
	}

	ipConfig := &types.HostIpConfig{}
	routeConfig := &types.HostIpRouteConfig{} // routeConfig := r.IpRouteConfig.GetHostIpRouteConfig()
	if ipv4, ok := d.GetOk("ipv4.0"); ok {
		ipv4Config := ipv4.(map[string]interface{})

		dhcp := ipv4Config["dhcp"].(bool)
		ipv4Address := ipv4Config["ip"].(string)
		ipv4Netmask := ipv4Config["netmask"].(string)
		ipv4Gateway := ipv4Config["gw"].(string)

		if dhcp {
			ipConfig.Dhcp = dhcp
		} else if ipv4Address != "" && ipv4Netmask != "" {
			ipConfig.IpAddress = ipv4Address
			ipConfig.SubnetMask = ipv4Netmask
			routeConfig.DefaultGateway = ipv4Gateway
		}
	}

	if ipv6, ok := d.GetOk("ipv6.0"); ok {
		ipv6Spec := &types.HostIpConfigIpV6AddressConfiguration{}
		ipv6Config := ipv6.(map[string]interface{})

		dhcpv6 := ipv6Config["dhcp"].(bool)
		autoconfig := ipv6Config["autoconfig"].(bool)
		// ipv6addrs := ipv6Config["addresses"].([]interface{})
		ipv6Gateway := ipv6Config["gw"].(string)
		ipv6Spec.DhcpV6Enabled = &dhcpv6
		ipv6Spec.AutoConfigurationEnabled = &autoconfig

		oldAddrsIntf, newAddrsIntf := d.GetChange("ipv6.0.addresses")
		oldAddrs := oldAddrsIntf.([]interface{})
		newAddrs := newAddrsIntf.([]interface{})
		addAddrs := make([]string, len(newAddrs))
		var removeAddrs []string

		// calculate addresses to remove
		for _, old := range oldAddrs {
			addrFound := false
			for _, newAddr := range newAddrs {
				if old == newAddr {
					addrFound = true
					break
				}
			}
			if !addrFound {
				removeAddrs = append(removeAddrs, old.(string))
			}
		}

		// calculate addresses to add
		for _, newAddr := range newAddrs {
			addrFound := false
			for _, old := range oldAddrs {
				if newAddr == old {
					addrFound = true
					break
				}
			}
			if !addrFound {
				addAddrs = append(addAddrs, newAddr.(string))
			}
		}

		if len(removeAddrs) > 0 || len(addAddrs) > 0 {
			addrs := make([]types.HostIpConfigIpV6Address, 0)
			for _, removeAddr := range removeAddrs {
				addrParts := strings.Split(removeAddr, "/")
				addr := addrParts[0]
				prefix, err := strconv.ParseInt(addrParts[1], 0, 32)
				if err != nil {
					return nil, fmt.Errorf("error while parsing IPv6 address")
				}
				tmpAddr := types.HostIpConfigIpV6Address{
					IpAddress:    strings.ToLower(addr),
					PrefixLength: int32(prefix),
					Origin:       "manual",
					Operation:    "remove",
				}
				addrs = append(addrs, tmpAddr)
			}

			for _, newAddr := range newAddrs {
				addrParts := strings.Split(newAddr.(string), "/")
				addr := addrParts[0]
				prefix, err := strconv.ParseInt(addrParts[1], 0, 32)
				if err != nil {
					return nil, fmt.Errorf("error while parsing IPv6 address")
				}
				tmpAddr := types.HostIpConfigIpV6Address{
					IpAddress:    strings.ToLower(addr),
					PrefixLength: int32(prefix),
					Origin:       "manual",
					Operation:    "add",
				}
				addrs = append(addrs, tmpAddr)
			}
			ipv6Spec.IpV6Address = addrs
		}
		routeConfig.IpV6DefaultGateway = ipv6Gateway
		ipConfig.IpV6Config = ipv6Spec
	}

	r := &types.HostVirtualNicIpRouteSpec{
		IpRouteConfig: routeConfig,
	}

	netStackInstance := d.Get("netstack").(string)

	vnic := &types.HostVirtualNicSpec{
		Ip:                     ipConfig,
		Mac:                    mac,
		Mtu:                    mtu,
		Portgroup:              portgroup,
		DistributedVirtualPort: dvpPortConnection,
		IpRouteSpec:            r,
		NetStackInstanceKey:    netStackInstance,
	}
	return vnic, nil
}

func getVnicFromHost(ctx context.Context, client *govmomi.Client, hostID, nicID string) (*types.HostVirtualNic, error) {
	host, err := hostsystem.FromID(client, hostID)
	if err != nil {
		return nil, err
	}

	var hostProps mo.HostSystem
	err = host.Properties(ctx, host.Reference(), nil, &hostProps)
	if err != nil {
		log.Printf("[DEBUG] Failed to get the host's properties: %s", err)
		return nil, err
	}
	vNics := hostProps.Config.Network.Vnic
	nicIdx := -1
	for idx, vnic := range vNics {
		log.Printf("[DEBUG] Evaluating nic: %s", vnic.Device)
		if vnic.Device == nicID {
			nicIdx = idx
			break
		}
	}

	if nicIdx == -1 {
		return nil, fmt.Errorf("vNic interface with id %s not found", nicID)
	}
	return &vNics[nicIdx], nil
}

func splitHostIDNicID(d *schema.ResourceData) (string, string) {
	idParts := strings.Split(d.Id(), "_")
	return idParts[0], idParts[1]
}
