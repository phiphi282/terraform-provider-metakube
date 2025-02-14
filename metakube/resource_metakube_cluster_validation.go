package metakube

import (
	"context"
	"fmt"
	"strings"

	"github.com/syseleven/go-metakube/client/project"

	"github.com/hashicorp/go-cty/cty"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/syseleven/go-metakube/client/openstack"
	"github.com/syseleven/go-metakube/client/versions"
	"github.com/syseleven/go-metakube/models"
)

type metakubeResourceClusterOpenstackValidationData struct {
	dcName                       *string
	domain                       *string
	username                     *string
	password                     *string
	tenant                       *string
	applicationCredentialsID     *string
	applicationCredentialsSecret *string
	network                      *string
	subnetID                     *string
}

type metakubeResourceClusterGeneralOpenstackRequestParams interface {
	SetDatacenterName(*string)
	SetCredential(*string)
	SetDomain(*string)
	SetUsername(*string)
	SetPassword(*string)
	SetTenant(*string)
	SetApplicationCredentialID(*string)
	SetApplicationCredentialSecret(*string)
	SetContext(context.Context)
}

func (data *metakubeResourceClusterOpenstackValidationData) setParams(ctx context.Context, p metakubeResourceClusterGeneralOpenstackRequestParams) {
	p.SetDatacenterName(data.dcName)
	p.SetDomain(data.domain)
	p.SetUsername(data.username)
	p.SetPassword(data.password)
	p.SetTenant(data.tenant)
	p.SetApplicationCredentialID(data.applicationCredentialsID)
	p.SetApplicationCredentialSecret(data.applicationCredentialsSecret)
	p.SetContext(ctx)
}

func newOpenstackValidationData(d *schema.ResourceData) metakubeResourceClusterOpenstackValidationData {
	return metakubeResourceClusterOpenstackValidationData{
		dcName:                       toStrPtrOrNil(d.Get("dc_name")),
		domain:                       strToPtr("Default"),
		username:                     toStrPtrOrNil(d.Get("spec.0.cloud.0.openstack.0.username")),
		password:                     toStrPtrOrNil(d.Get("spec.0.cloud.0.openstack.0.password")),
		tenant:                       toStrPtrOrNil(d.Get("spec.0.cloud.0.openstack.0.tenant")),
		applicationCredentialsID:     toStrPtrOrNil(d.Get("spec.0.cloud.0.openstack.0.application_credentials_id")),
		applicationCredentialsSecret: toStrPtrOrNil(d.Get("spec.0.cloud.0.openstack.0.application_credentials_secret")),
		network:                      toStrPtrOrNil(d.Get("spec.0.cloud.0.openstack.0.network")),
		subnetID:                     toStrPtrOrNil(d.Get("spec.0.cloud.0.openstack.0.subnet_id")),
	}
}

func toStrPtrOrNil(v interface{}) *string {
	if v == nil {
		return nil
	}
	return strToPtr(v.(string))
}

func metakubeResourceClusterValidateClusterFields(ctx context.Context, d *schema.ResourceData, k *metakubeProviderMeta) diag.Diagnostics {
	ret := metakubeResourceValidateVersionExistence(ctx, d, k)
	if _, ok := d.GetOk("spec.0.cloud.0.openstack.0"); !ok {
		return ret
	}
	ret = append(ret, metakubeResourceClusterValidateFloatingIPPool(ctx, d, k)...)
	ret = append(ret, metakubeResourceClusterValidateOpenstackNetwork(ctx, d, k)...)
	ret = append(ret, metakubeResourceClusterValidateAccessCredentialsSet(d)...)
	return append(ret, diagnoseOpenstackSubnetWithIDExistsIfSet(ctx, d, k)...)
}

func metakubeResourceClusterValidateVersionUpgrade(ctx context.Context, projectID, newVersion string, cluster *models.Cluster, k *metakubeProviderMeta) diag.Diagnostics {
	p := project.NewGetClusterUpgradesV2Params().
		WithContext(ctx).
		WithProjectID(projectID).
		WithClusterID(cluster.ID)
	r, err := k.client.Project.GetClusterUpgradesV2(p, k.auth)
	if err != nil {
		return diag.FromErr(err)
	}
	var available []string
	for _, item := range r.Payload {
		v := item.Version.(string)
		available = append(available, v)
		if v == newVersion {
			return nil
		}
	}
	return diag.Diagnostics{{
		Severity:      diag.Error,
		Summary:       fmt.Sprintf("not allowed upgrade %s->%s", cluster.Spec.Version, newVersion),
		AttributePath: cty.GetAttrPath("spec").IndexInt(0).GetAttr("version"),
		Detail:        fmt.Sprintf("Please select one of available upgrades: %v", available),
	}}
}

func metakubeResourceValidateVersionExistence(ctx context.Context, d *schema.ResourceData, k *metakubeProviderMeta) diag.Diagnostics {
	version := d.Get("spec.0.version").(string)
	p := versions.NewGetMasterVersionsParams().WithContext(ctx)
	r, err := k.client.Versions.GetMasterVersions(p, k.auth)
	if err != nil {
		return diag.Errorf("%s", stringifyResponseError(err))
	}

	available := make([]string, 0)
	for _, v := range r.Payload {
		available = append(available, v.Version.(string))
		if v.Version.(string) == version {
			return nil
		}
	}

	return diag.Diagnostics{{
		Severity:      diag.Error,
		Summary:       fmt.Sprintf("unknown version %s", version),
		AttributePath: cty.GetAttrPath("spec").IndexInt(0).GetAttr("version"),
		Detail:        fmt.Sprintf("Please select one of available versions: %v", available),
	}}
}

func metakubeResourceClusterValidateFloatingIPPool(ctx context.Context, d *schema.ResourceData, k *metakubeProviderMeta) diag.Diagnostics {
	nets, err := validateOpenstackNetworkExistsIfSet(ctx, d, k, "spec.0.cloud.0.openstack.0.floating_ip_pool", true)
	if err != nil {
		var diagnoseDetail string
		if len(nets) > 0 {
			names := make([]string, 0)
			for _, n := range nets {
				if n.External {
					names = append(names, n.Name)
				}
			}
			diagnoseDetail = fmt.Sprintf("We found following floating IP pools: %v", names)
		}
		return diag.Diagnostics{{
			Severity:      diag.Error,
			Summary:       fmt.Sprintf("invalid value: %v", err),
			AttributePath: cty.GetAttrPath("spec").IndexInt(0).GetAttr("cloud").IndexInt(0).GetAttr("openstack").IndexInt(0).GetAttr("floating_ip_pool"),
			Detail:        diagnoseDetail,
		}}
	}
	return nil
}

func metakubeResourceClusterValidateAccessCredentialsSet(d *schema.ResourceData) diag.Diagnostics {
	data := newOpenstackValidationData(d)
	username := data.username != nil && *data.username != ""
	password := data.password != nil && *data.password != ""
	tenant := data.tenant != nil && *data.tenant != ""
	applicationCredentialsID := data.applicationCredentialsID != nil && *data.applicationCredentialsID != ""
	applicationCredentialsSecret := data.applicationCredentialsSecret != nil && *data.applicationCredentialsSecret != ""

	if (username || password || tenant) && (applicationCredentialsID || applicationCredentialsSecret) {
		return diag.Diagnostics{{
			Severity:      diag.Error,
			Summary:       "Please use either username, password, tenant or application_credentials_id, application_credentials_secret, not both",
			AttributePath: cty.GetAttrPath("spec").IndexInt(0).GetAttr("cloud").IndexInt(0).GetAttr("openstack").IndexInt(0),
		}}
	}

	if (username || password || tenant) && (!username || !password || !tenant) {
		var details []string
		if !username {
			details = append(details, "username not set")
		}
		if !password {
			details = append(details, "password not set")
		}
		if !tenant {
			details = append(details, "tenant not set")
		}
		return diag.Diagnostics{{
			Severity:      diag.Error,
			Summary:       "Please set all username, password, tenant fields or use application_credentials_id, application_credentials_secret fields",
			AttributePath: cty.GetAttrPath("spec").IndexInt(0).GetAttr("cloud").IndexInt(0).GetAttr("openstack").IndexInt(0),
			Detail:        strings.Join(details, ", "),
		}}
	}

	if (applicationCredentialsID || applicationCredentialsSecret) && (!applicationCredentialsID || !applicationCredentialsSecret) {
		var details []string
		if !applicationCredentialsID {
			details = append(details, "application_credentials_id not set")
		}
		if !applicationCredentialsSecret {
			details = append(details, "application_credentials_secret not set")
		}
		return diag.Diagnostics{{
			Severity:      diag.Error,
			Summary:       "Please set both application_credentials_id, application_credentials_secret fields",
			AttributePath: cty.GetAttrPath("spec").IndexInt(0).GetAttr("cloud").IndexInt(0).GetAttr("openstack").IndexInt(0),
			Detail:        strings.Join(details, ", "),
		}}
	}

	return nil
}

func metakubeResourceClusterValidateOpenstackNetwork(ctx context.Context, d *schema.ResourceData, k *metakubeProviderMeta) diag.Diagnostics {
	allnets, err := validateOpenstackNetworkExistsIfSet(ctx, d, k, "spec.0.cloud.0.openstack.0.network", false)
	if err != nil {
		names := make([]string, 0)
		for _, n := range allnets {
			if n.External == false {
				names = append(names, n.Name)
			}
		}
		var diagnoseDetail string
		if len(names) > 0 {
			diagnoseDetail = fmt.Sprintf("We found following networks: %v", names)
		}
		return diag.Diagnostics{{
			Severity:      diag.Error,
			Summary:       fmt.Sprintf("invalid value: %v", err),
			AttributePath: cty.GetAttrPath("spec").IndexInt(0).GetAttr("cloud").IndexInt(0).GetAttr("openstack").IndexInt(0).GetAttr("network"),
			Detail:        diagnoseDetail,
		}}
	}
	return nil
}

func validateOpenstackNetworkExistsIfSet(ctx context.Context, d *schema.ResourceData, k *metakubeProviderMeta, field string, external bool) ([]*models.OpenstackNetwork, error) {
	value, ok := d.GetOk(field)
	if !ok {
		return nil, nil
	}

	data := newOpenstackValidationData(d)
	_, all, err := getNetwork(ctx, k, data, value.(string), external)
	return all, err
}

func diagnoseOpenstackSubnetWithIDExistsIfSet(ctx context.Context, d *schema.ResourceData, k *metakubeProviderMeta) diag.Diagnostics {
	data := newOpenstackValidationData(d)
	if data.network == nil || data.subnetID == nil {
		return nil
	}
	network, _, err := getNetwork(ctx, k, data, *data.network, true)
	if err != nil {
		return nil
	}

	subnets, ok, err := getSubnet(ctx, k, data, network.ID)
	if ok {
		return nil
	}
	var diagnoseDetail string
	if len(subnets) > 0 {
		tmp := make([]string, 0)
		for _, i := range subnets {
			tmp = append(tmp, fmt.Sprintf("%s/%s", i.Name, i.ID))
		}
		diagnoseDetail = fmt.Sprintf("We found following subnets (name/id): %v", tmp)
	}
	return diag.Diagnostics{{
		Severity:      diag.Error,
		Summary:       fmt.Sprintf("invalid value: %v", err),
		AttributePath: cty.GetAttrPath("spec").IndexInt(0).GetAttr("cloud").IndexInt(0).GetAttr("openstack").IndexInt(0).GetAttr("subnetID"),
		Detail:        diagnoseDetail,
	}}
}

func getNetwork(ctx context.Context, k *metakubeProviderMeta, data metakubeResourceClusterOpenstackValidationData, name string, external bool) (*models.OpenstackNetwork, []*models.OpenstackNetwork, error) {
	p := openstack.NewListOpenstackNetworksParams()
	data.setParams(ctx, p)
	res, err := k.client.Openstack.ListOpenstackNetworks(p, k.auth)
	if err != nil {
		return nil, nil, fmt.Errorf("find network instance %v", stringifyResponseError(err))
	}
	ret := findNetwork(res.Payload, name, external)
	if ret == nil {
		return nil, res.Payload, fmt.Errorf("network `%s` not found", name)
	}
	return ret, res.Payload, nil
}

func findNetwork(list []*models.OpenstackNetwork, network string, external bool) *models.OpenstackNetwork {
	for _, item := range list {
		if item.Name == network && item.External == external {
			return item
		}
	}
	return nil
}

func getSubnet(ctx context.Context, k *metakubeProviderMeta, data metakubeResourceClusterOpenstackValidationData, networkID string) ([]*models.OpenstackSubnet, bool, error) {
	p := openstack.NewListOpenstackSubnetsParams()
	data.setParams(ctx, p)
	p.SetNetworkID(&networkID)
	res, err := k.client.Openstack.ListOpenstackSubnets(p, k.auth)
	if err != nil {
		return nil, false, fmt.Errorf("list network subnets: %v", stringifyResponseError(err))
	}
	return res.Payload, findSubnet(res.Payload, *data.subnetID) != nil, nil
}

func findSubnet(list []*models.OpenstackSubnet, id string) *models.OpenstackSubnet {
	for _, item := range list {
		if item.ID == id {
			return item
		}
	}
	return nil
}
