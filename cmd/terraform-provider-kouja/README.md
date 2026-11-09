# Terraform provider for kouja

The provider is built from this repository:

```sh
make build-provider
```

Install `bin/terraform-provider-kouja` through a filesystem mirror, a private
registry, or `dev_overrides`. The public Terraform Registry is not a target.

The provider requires an admin client certificate. Configure the agent and
provider with mTLS credentials; see the examples in `examples/`.

Resources:

- `kouja_volume_group` registers and observes an existing LVM volume group.
- `kouja_storage_class` maps a storage class to a registered volume group.
- `kouja_client_certificate` issues and revokes a client certificate. Its
  private key is marked sensitive and is stored in Terraform state.

`terraform destroy` revokes certificates. The provider refuses to revoke its
own admin certificate. `IssueCertificate` is not retried; a lost response can
leave an orphan that is visible through the agent certificate list.
