package azurerm

import (
	"fmt"
	"log"
	"regexp"

	"github.com/hashicorp/terraform/helper/schema"
	"github.com/hashicorp/terraform/helper/validation"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/helpers/azure"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/helpers/tf"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/helpers/validate"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/internal/services/storage"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/utils"
	"github.com/tombuildsstuff/giovanni/storage/2018-11-09/file/shares"
)

func resourceArmStorageShare() *schema.Resource {
	return &schema.Resource{
		Create: resourceArmStorageShareCreate,
		Read:   resourceArmStorageShareRead,
		Update: resourceArmStorageShareUpdate,
		Delete: resourceArmStorageShareDelete,
		Importer: &schema.ResourceImporter{
			State: schema.ImportStatePassthrough,
		},
		SchemaVersion: 2,
		MigrateState:  resourceStorageShareMigrateState,

		Schema: map[string]*schema.Schema{
			"name": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validateArmStorageShareName,
			},

			"resource_group_name": azure.SchemaResourceGroupNameDeprecated(),

			"storage_account_name": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},

			"quota": {
				Type:         schema.TypeInt,
				Optional:     true,
				Default:      5120,
				ValidateFunc: validation.IntBetween(1, 5120),
			},

			"metadata": storage.MetaDataSchema(),

			"acl": {
				Type:     schema.TypeSet,
				Optional: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id": {
							Type:         schema.TypeString,
							Required:     true,
							ValidateFunc: validation.StringLenBetween(1, 64),
						},
						"access_policy": {
							Type:     schema.TypeList,
							Optional: true,
							Elem: &schema.Resource{
								Schema: map[string]*schema.Schema{
									"start": {
										Type:         schema.TypeString,
										Required:     true,
										ValidateFunc: validate.NoEmptyStrings,
									},
									"expiry": {
										Type:         schema.TypeString,
										Required:     true,
										ValidateFunc: validate.NoEmptyStrings,
									},
									"permissions": {
										Type:         schema.TypeString,
										Required:     true,
										ValidateFunc: validate.NoEmptyStrings,
									},
								},
							},
						},
					},
				},
			},

			"url": {
				Type:     schema.TypeString,
				Computed: true,
			},
		},
	}
}
func resourceArmStorageShareCreate(d *schema.ResourceData, meta interface{}) error {
	ctx := meta.(*ArmClient).StopContext
	storageClient := meta.(*ArmClient).storage

	accountName := d.Get("storage_account_name").(string)
	shareName := d.Get("name").(string)
	quota := d.Get("quota").(int)

	metaDataRaw := d.Get("metadata").(map[string]interface{})
	metaData := storage.ExpandMetaData(metaDataRaw)

	aclsRaw := d.Get("acl").(*schema.Set).List()
	acls := expandStorageShareACLs(aclsRaw)

	resourceGroup, err := storageClient.FindResourceGroup(ctx, accountName)
	if err != nil {
		return fmt.Errorf("Error locating Resource Group: %s", err)
	}

	client, err := storageClient.FileSharesClient(ctx, *resourceGroup, accountName)
	if err != nil {
		return fmt.Errorf("Error building File Share Client: %s", err)
	}

	id := client.GetResourceID(accountName, shareName)

	if requireResourcesToBeImported {
		existing, err := client.GetProperties(ctx, accountName, shareName)
		if err != nil {
			if !utils.ResponseWasNotFound(existing.Response) {
				return fmt.Errorf("Error checking for existence of existing Storage Share %q (Account %q / Resource Group %q): %+v", shareName, accountName, *resourceGroup, err)
			}
		}

		if !utils.ResponseWasNotFound(existing.Response) {
			return tf.ImportAsExistsError("azurerm_storage_share", id)
		}
	}

	log.Printf("[INFO] Creating Share %q in Storage Account %q", shareName, accountName)
	input := shares.CreateInput{
		QuotaInGB: quota,
		MetaData:  metaData,
	}
	if _, err := client.Create(ctx, accountName, shareName, input); err != nil {
		return fmt.Errorf("Error creating Share %q (Account %q / Resource Group %q): %+v", shareName, accountName, *resourceGroup, err)
	}

	if _, err := client.SetACL(ctx, accountName, shareName, acls); err != nil {
		return fmt.Errorf("Error setting ACL's for Share %q (Account %q / Resource Group %q): %+v", shareName, accountName, *resourceGroup, err)
	}

	d.SetId(id)
	return resourceArmStorageShareRead(d, meta)
}

func resourceArmStorageShareRead(d *schema.ResourceData, meta interface{}) error {
	ctx := meta.(*ArmClient).StopContext
	storageClient := meta.(*ArmClient).storage

	id, err := shares.ParseResourceID(d.Id())
	if err != nil {
		return err
	}

	resourceGroup, err := storageClient.FindResourceGroup(ctx, id.AccountName)
	if err != nil {
		return fmt.Errorf("Error locating Resource Group for Storage Account %q: %s", id.AccountName, err)
	}
	if resourceGroup == nil {
		log.Printf("[DEBUG] Unable to locate Resource Group for Storage Account %q - assuming removed & removing from state", id.AccountName)
		d.SetId("")
		return nil
	}

	client, err := storageClient.FileSharesClient(ctx, *resourceGroup, id.AccountName)
	if err != nil {
		return fmt.Errorf("Error building File Share Client for Storage Account %q (Resource Group %q): %s", id.AccountName, *resourceGroup, err)
	}

	props, err := client.GetProperties(ctx, id.AccountName, id.ShareName)
	if err != nil {
		if utils.ResponseWasNotFound(props.Response) {
			log.Printf("[DEBUG] File Share %q was not found in Account %q / Resource Group %q - assuming removed & removing from state", id.ShareName, id.AccountName, *resourceGroup)
			d.SetId("")
			return nil
		}

		return fmt.Errorf("Error retrieving File Share %q (Account %q / Resource Group %q): %s", id.ShareName, id.AccountName, *resourceGroup, err)
	}

	acls, err := client.GetACL(ctx, id.AccountName, id.ShareName)
	if err != nil {
		return fmt.Errorf("Error retrieving ACL's for File Share %q (Account %q / Resource Group %q): %s", id.ShareName, id.AccountName, *resourceGroup, err)
	}

	d.Set("name", id.ShareName)
	d.Set("storage_account_name", id.AccountName)
	d.Set("url", client.GetResourceID(id.AccountName, id.ShareName))
	d.Set("quota", props.ShareQuota)

	if err := d.Set("metadata", storage.FlattenMetaData(props.MetaData)); err != nil {
		return fmt.Errorf("Error flattening `metadata`: %+v", err)
	}

	if err := d.Set("acl", flattenStorageShareACLs(acls)); err != nil {
		return fmt.Errorf("Error flattening `acl`: %+v", err)
	}

	// Deprecated: remove in 2.0
	d.Set("resource_group_name", resourceGroup)

	return nil
}

func resourceArmStorageShareUpdate(d *schema.ResourceData, meta interface{}) error {
	ctx := meta.(*ArmClient).StopContext
	storageClient := meta.(*ArmClient).storage

	id, err := shares.ParseResourceID(d.Id())
	if err != nil {
		return err
	}

	resourceGroup, err := storageClient.FindResourceGroup(ctx, id.AccountName)
	if err != nil {
		return fmt.Errorf("Error locating Resource Group for Storage Account %q: %s", id.AccountName, err)
	}
	if resourceGroup == nil {
		log.Printf("[DEBUG] Unable to locate Resource Group for Storage Account %q - assuming removed & removing from state", id.AccountName)
		d.SetId("")
		return nil
	}

	client, err := storageClient.FileSharesClient(ctx, *resourceGroup, id.AccountName)
	if err != nil {
		return fmt.Errorf("Error building File Share Client for Storage Account %q (Resource Group %q): %s", id.AccountName, *resourceGroup, err)
	}

	if d.HasChange("quota") {
		log.Printf("[DEBUG] Updating the Quota for File Share %q (Storage Account %q)", id.ShareName, id.AccountName)
		quota := d.Get("quota").(int)
		if _, err := client.SetProperties(ctx, id.AccountName, id.ShareName, quota); err != nil {
			return fmt.Errorf("Error updating Quota for File Share %q (Storage Account %q): %s", id.ShareName, id.AccountName, err)
		}

		log.Printf("[DEBUG] Updated the Quota for File Share %q (Storage Account %q)", id.ShareName, id.AccountName)
	}

	if d.HasChange("metadata") {
		log.Printf("[DEBUG] Updating the MetaData for File Share %q (Storage Account %q)", id.ShareName, id.AccountName)

		metaDataRaw := d.Get("metadata").(map[string]interface{})
		metaData := storage.ExpandMetaData(metaDataRaw)

		if _, err := client.SetMetaData(ctx, id.AccountName, id.ShareName, metaData); err != nil {
			return fmt.Errorf("Error updating MetaData for File Share %q (Storage Account %q): %s", id.ShareName, id.AccountName, err)
		}

		log.Printf("[DEBUG] Updated the MetaData for File Share %q (Storage Account %q)", id.ShareName, id.AccountName)
	}

	if d.HasChange("acl") {
		log.Printf("[DEBUG] Updating the ACL's for File Share %q (Storage Account %q)", id.ShareName, id.AccountName)

		aclsRaw := d.Get("acl").(*schema.Set).List()
		acls := expandStorageShareACLs(aclsRaw)

		if _, err := client.SetACL(ctx, id.AccountName, id.ShareName, acls); err != nil {
			return fmt.Errorf("Error updating ACL's for File Share %q (Storage Account %q): %s", id.ShareName, id.AccountName, err)
		}

		log.Printf("[DEBUG] Updated the ACL's for File Share %q (Storage Account %q)", id.ShareName, id.AccountName)
	}

	return resourceArmStorageShareRead(d, meta)
}

func resourceArmStorageShareDelete(d *schema.ResourceData, meta interface{}) error {
	ctx := meta.(*ArmClient).StopContext
	storageClient := meta.(*ArmClient).storage

	id, err := shares.ParseResourceID(d.Id())
	if err != nil {
		return err
	}

	resourceGroup, err := storageClient.FindResourceGroup(ctx, id.AccountName)
	if err != nil {
		return fmt.Errorf("Error locating Resource Group for Storage Account %q: %s", id.AccountName, err)
	}
	if resourceGroup == nil {
		log.Printf("[DEBUG] Unable to locate Resource Group for Storage Account %q - assuming removed & removing from state", id.AccountName)
		d.SetId("")
		return nil
	}

	client, err := storageClient.FileSharesClient(ctx, *resourceGroup, id.AccountName)
	if err != nil {
		return fmt.Errorf("Error building File Share Client for Storage Account %q (Resource Group %q): %s", id.AccountName, *resourceGroup, err)
	}

	deleteSnapshots := true
	if _, err := client.Delete(ctx, id.AccountName, id.ShareName, deleteSnapshots); err != nil {
		return fmt.Errorf("Error deleting File Share %q (Storage Account %q / Resource Group %q): %s", id.ShareName, id.AccountName, *resourceGroup, err)
	}

	return nil
}

func expandStorageShareACLs(input []interface{}) []shares.SignedIdentifier {
	results := make([]shares.SignedIdentifier, 0)

	for _, v := range input {
		vals := v.(map[string]interface{})

		policies := vals["access_policy"].([]interface{})
		policy := policies[0].(map[string]interface{})

		identifier := shares.SignedIdentifier{
			Id: vals["id"].(string),
			AccessPolicy: shares.AccessPolicy{
				Start:      policy["start"].(string),
				Expiry:     policy["expiry"].(string),
				Permission: policy["permissions"].(string),
			},
		}
		results = append(results, identifier)
	}

	return results
}

func flattenStorageShareACLs(input shares.GetACLResult) []interface{} {
	result := make([]interface{}, 0)

	for _, v := range input.SignedIdentifiers {
		output := map[string]interface{}{
			"id": v.Id,
			"access_policy": []interface{}{
				map[string]interface{}{
					"start":       v.AccessPolicy.Start,
					"expiry":      v.AccessPolicy.Expiry,
					"permissions": v.AccessPolicy.Permission,
				},
			},
		}

		result = append(result, output)
	}

	return result
}

// Following the naming convention as laid out in the docs https://msdn.microsoft.com/library/azure/dn167011.aspx
func validateArmStorageShareName(v interface{}, k string) (warnings []string, errors []error) {
	value := v.(string)
	if !regexp.MustCompile(`^[0-9a-z-]+$`).MatchString(value) {
		errors = append(errors, fmt.Errorf(
			"only lowercase alphanumeric characters and hyphens allowed in %q: %q",
			k, value))
	}
	if len(value) < 3 || len(value) > 63 {
		errors = append(errors, fmt.Errorf(
			"%q must be between 3 and 63 characters: %q", k, value))
	}
	if regexp.MustCompile(`^-`).MatchString(value) {
		errors = append(errors, fmt.Errorf(
			"%q cannot begin with a hyphen: %q", k, value))
	}
	if regexp.MustCompile(`[-]{2,}`).MatchString(value) {
		errors = append(errors, fmt.Errorf(
			"%q does not allow consecutive hyphens: %q", k, value))
	}
	return warnings, errors
}
